package export

// Incus export basename drift: where the VM disk lives inside an instance
// archive has moved between releases (backup/virtual-machine.img vs
// backup/root.img), and the adapter matches on the basename so a third
// spelling or a new prefix keeps working. These tests pin that the choice is
// always an explicit disk or an explicit refusal — never a silent wrong file.

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarMember struct {
	name string
	body string
	// dir marks a directory entry: it must never be mistaken for a disk even
	// when its name collides with a disk basename.
	dir bool
}

// writeTarOrdered builds an archive with members in the given order (unlike
// writeTar, whose map iteration is random — order matters here because the
// extractor returns the first disk match).
func writeTarOrdered(t *testing.T, path string, members []tarMember) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w := tar.NewWriter(f)
	for _, m := range members {
		flag := byte(tar.TypeReg)
		var size int64
		if m.dir {
			flag = byte(tar.TypeDir)
		} else {
			size = int64(len(m.body))
		}
		if err := w.WriteHeader(&tar.Header{
			Name: m.name, Mode: 0o600, Size: size, Typeflag: flag,
		}); err != nil {
			t.Fatal(err)
		}
		if !m.dir {
			if _, err := w.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func readDest(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// A new prefix must not break extraction: only the basename is significant.
func TestExtractInstanceDisk_AcceptsANewPrefix(t *testing.T) {
	for name, members := range map[string][]tarMember{
		"top level": {
			{name: "virtual-machine.img", body: "DISK-TOP"},
			{name: "backup/index.yaml", body: "name: dev\n"},
		},
		"deeply nested": {
			{name: "backup/index.yaml", body: "name: dev\n"},
			{name: "backup/disks/0/virtual-machine.img", body: "DISK-DEEP"},
		},
		"root.img under a new prefix": {
			{name: "export/root.img", body: "DISK-ROOT"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "inst.tar")
			writeTarOrdered(t, archive, members)
			dest := filepath.Join(dir, "disk.img")
			if err := extractInstanceDisk(archive, dest); err != nil {
				t.Fatalf("extractInstanceDisk: %v", err)
			}
			if got := readDest(t, dest); !strings.HasPrefix(got, "DISK-") {
				t.Fatalf("extracted %q, want the disk's contents", got)
			}
		})
	}
}

// Both historic names in one archive: the extractor returns the first match
// in tar order. Either disk is a defensible answer; what must never happen is
// an error, an empty file, or a non-disk member.
func TestExtractInstanceDisk_FirstMatchWinsWhenBothNamesPresent(t *testing.T) {
	for name, members := range map[string][]tarMember{
		"virtual-machine first": {
			{name: "backup/virtual-machine.img", body: "NEWDISK"},
			{name: "backup/root.img", body: "OLDDISK"},
		},
		"root first": {
			{name: "backup/root.img", body: "OLDDISK"},
			{name: "backup/virtual-machine.img", body: "NEWDISK"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "inst.tar")
			writeTarOrdered(t, archive, members)
			dest := filepath.Join(dir, "disk.img")
			if err := extractInstanceDisk(archive, dest); err != nil {
				t.Fatalf("extractInstanceDisk: %v", err)
			}
			want := members[0].body
			if got := readDest(t, dest); got != want {
				t.Fatalf("extracted %q, want first disk match %q", got, want)
			}
		})
	}
}

// Near-miss names must not match: extracting virtual-machine.img.bak as the
// boot disk would hand the move pipeline a backup or temp file as a disk.
func TestExtractInstanceDisk_IgnoresDecoyNames(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "inst.tar")
	writeTarOrdered(t, archive, []tarMember{
		{name: "backup/index.yaml", body: "name: dev\n"},
		{name: "backup/virtual-machine.img.bak", body: "NOT-A-DISK"},
		{name: "backup/root.img.tmp", body: "NOT-A-DISK"},
		{name: "backup/xvirtual-machine.img", body: "NOT-A-DISK"},
		{name: "backup/virtual-machine.img/", body: "", dir: true},
		{name: "notes.txt", body: "NOT-A-DISK"},
	})
	dest := filepath.Join(dir, "disk.img")
	err := extractInstanceDisk(archive, dest)
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("want an explicit refusal, got %v (dest may hold a decoy)", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("a decoy was extracted as the disk: refusal must leave no file behind")
	}
}

// A directory entry with a disk's name is not a disk. GNU tar lists directory
// members with a trailing slash, but a hand-built archive may not — the type
// flag is what decides.
func TestExtractInstanceDisk_SkipsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "inst.tar")
	writeTarOrdered(t, archive, []tarMember{
		{name: "backup/virtual-machine.img", body: "", dir: true},
		{name: "backup/root.img", body: "REALDISK"},
	})
	dest := filepath.Join(dir, "disk.img")
	if err := extractInstanceDisk(archive, dest); err != nil {
		t.Fatalf("extractInstanceDisk: %v", err)
	}
	if got := readDest(t, dest); got != "REALDISK" {
		t.Fatalf("extracted %q, want the regular file's contents", got)
	}
}

// A container archive has no disk image at all: the refusal must name the
// cause (container, not VM) and offer a way forward — including when the
// archive also carries files a basename match could trip over elsewhere.
func TestExtractInstanceDisk_RefusesAContainerWithLookalikes(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ct.tar")
	writeTarOrdered(t, archive, []tarMember{
		{name: "backup/index.yaml", body: "name: ct\n"},
		{name: "backup/container/rootfs/etc/hostname", body: "ct\n"},
		{name: "backup/container/virtual-machine.img.bak", body: "NOT-A-DISK"},
	})
	err := extractInstanceDisk(archive, filepath.Join(dir, "disk.img"))
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("want a typed refusal, got %T: %v", err, err)
	}
	if !strings.Contains(u.Reason, "container") {
		t.Errorf("refusal does not name the cause: %q", u.Reason)
	}
	if u.Remedy == "" {
		t.Error("refusal offers no way forward")
	}
}
