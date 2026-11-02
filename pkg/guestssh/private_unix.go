//go:build linux || darwin

package guestssh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk descriptors so no path component follows a symlink. Every ancestor must
// prevent other users replacing the next entry; trusted sticky directories
// such as /tmp are safe because their entries cannot be renamed by other users.
// Existing directories are never chmodded to conceal unsafe state.
func openDirectory(path string, create bool) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == string(filepath.Separator) {
		return nil, errors.New("a non-root private directory is required")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := checkDirectory(fd, false, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(absolute, "/"), "/")
	for i, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if err := unix.Mkdirat(fd, part, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				_ = unix.Close(fd)
				return nil, err
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errors.New("refusing inaccessible or linked private directory")
		}
		fd = next
		if err := checkDirectory(fd, i == len(parts)-1, create); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
	}
	return os.NewFile(uintptr(fd), absolute), nil
}

func checkDirectory(fd int, final, private bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	uid, mode := uint32(os.Getuid()), uint32(stat.Mode)
	if stat.Uid != uid && (final || stat.Uid != 0) {
		return errors.New("refusing an SSH directory owned by an untrusted user")
	}
	mask := uint32(0o022)
	if final && private {
		mask = 0o077
	}
	if mode&mask != 0 && (final || mode&unix.S_ISVTX == 0) {
		return errors.New("refusing a non-private SSH directory or writable ancestor")
	}
	return nil
}

func privateDirectory(path string) error {
	directory, err := openDirectory(path, true)
	if err != nil {
		return err
	}
	return directory.Close()
}

func privateFile(path string, create bool) error {
	directory, err := openDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	fd, err := unix.Openat(int(directory.Fd()), filepath.Base(path), flags, 0)
	if errors.Is(err, unix.ENOENT) && create {
		fd, err = unix.Openat(int(directory.Fd()), filepath.Base(path), flags|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(int(directory.Fd()), filepath.Base(path), flags, 0)
		}
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || stat.Mode&0o077 != 0 {
		return errors.New("refusing non-owned, linked, non-private or non-regular SSH file")
	}
	return nil
}
