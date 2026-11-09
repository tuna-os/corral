//go:build linux || darwin

package macos

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && stat.Nlink == 1
}

// Walk directory descriptors, never following a symlink in any path component.
// Existing ancestors are not chmodded; only the requested private directory and
// directories we create belong to this helper.
func openPrivateDirectory(path string, create bool) (*os.File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("a private directory path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == string(filepath.Separator) {
		return nil, errors.New("a non-root private directory is required")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("cannot open private directory root")
	}
	parts := strings.Split(strings.TrimPrefix(absolute, "/"), "/")
	for i, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if err := unix.Mkdirat(fd, part, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				_ = unix.Close(fd)
				return nil, errors.New("cannot create private directory")
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errors.New("refusing inaccessible or linked private directory")
		}
		fd = next
		if i == len(parts)-1 {
			var stat unix.Stat_t
			if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
				_ = unix.Close(fd)
				return nil, errors.New("refusing non-owned private directory")
			}
			if !create && stat.Mode&0o022 != 0 {
				_ = unix.Close(fd)
				return nil, errors.New("refusing a writable-by-others private file directory")
			}
			if create {
				if err := unix.Fchmod(fd, 0o700); err != nil {
					_ = unix.Close(fd)
					return nil, errors.New("cannot restrict private directory")
				}
			}
		}
	}
	return os.NewFile(uintptr(fd), absolute), nil
}

func privateDirectory(path string) error {
	directory, err := openPrivateDirectory(path, true)
	if err != nil {
		return err
	}
	return directory.Close()
}

func privateOpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	directory, err := openPrivateDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), filepath.Base(path), flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, fs.ErrNotExist
		}
		if errors.Is(err, unix.EEXIST) {
			return nil, fs.ErrExist
		}
		return nil, errors.New("refusing inaccessible or linked private file")
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
		_ = file.Close()
		return nil, errors.New("refusing non-owned, linked or non-regular private file")
	}
	return file, nil
}

func privateConfig(path string) error {
	file, err := privateOpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return errors.New("cannot restrict private authentication config")
	}
	return nil
}

func lockPrivateState(ctx context.Context, directory string) (func(), error) {
	file, err := privateOpenFile(filepath.Join(directory, ".prepare.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, errors.New("cannot restrict private preparation lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = file.Close() }, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, errors.New("cannot lock private preparation state")
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func bitwardenSessionPath() string {
	// Darwin's system-owned /tmp symlink is not a user-controlled cache path.
	if runtime.GOOS == "darwin" {
		return "/private/tmp/bw_session"
	}
	return "/tmp/bw_session"
}

func openBitwardenSession() (*os.File, error) {
	fd, err := unix.Open(bitwardenSessionPath(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, fs.ErrNotExist
		}
		return nil, errors.New("cannot read private Bitwarden session")
	}
	file := os.NewFile(uintptr(fd), "Bitwarden session")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("refusing a non-private, linked or non-owned Bitwarden session")
	}
	return file, nil
}
