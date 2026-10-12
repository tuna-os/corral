package catalog

import (
	"strings"
	"testing"
)

// Find and ResolveBootc resolve catalog names that arrive from CLI flags,
// API payloads, and dotfiles: Corral does not get to choose what a user
// types. A lookup must never panic, must agree with the catalog it searches,
// and — for ResolveBootc — must pass non-catalog refs through byte-identical,
// because that string becomes the image a VM boots.
//
// Run longer with: go test -run xxx -fuzz FuzzFind ./pkg/catalog/
// Run longer with: go test -run xxx -fuzz FuzzResolveBootc ./pkg/catalog/
func FuzzFind(f *testing.F) {
	for _, seed := range []string{
		"",
		"fedora-workstation",
		"ubuntu-desktop",
		"turnkey-wordpress",
		"windows-11",
		"bluefin-iso",
		"UBUNTU-DESKTOP",
		" ubuntu-desktop",
		"ubuntu-desktop\n",
		"../..",
		"fedora-workstation\x00",
		"%s",
		strings.Repeat("a", 4096),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Find(s)
		if got == nil {
			// A miss must be a real miss, not a skipped entry.
			for i := range Images {
				if Images[i].Name == s {
					t.Errorf("Find(%q) = nil, but Images[%d] carries that name", s, i)
				}
			}
			return
		}
		if got.Name != s {
			t.Errorf("Find(%q).Name = %q, want the queried name", s, got.Name)
		}
	})
}

func FuzzResolveBootc(f *testing.F) {
	for _, seed := range []string{
		"",
		"fedora-bootc",
		"aurora",
		"bluefin-dx",
		"quay.io/fedora/fedora-bootc:42",
		"ghcr.io/ublue-os/bluefin:latest",
		"docker.io/library/ubuntu:24.04",
		"fedora-bootc ",
		" FEDORA-BOOTC",
		"fedora-bootc\x00",
		"../evil",
		"https://evil.example/img:latest",
		"%2e%2e",
		":",
		"fedora-bootc:42@sha256:" + strings.Repeat("0", 64),
		strings.Repeat("a/", 1024),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := ResolveBootc(s)
		if b := FindBootc(s); b != nil {
			// A catalog name resolves to its pinned OCI ref.
			if got != b.Image {
				t.Errorf("ResolveBootc(%q) = %q, want catalog image %q", s, got, b.Image)
			}
			return
		}
		// Anything else is already a ref and must pass through unchanged —
		// rewriting it would boot a different image than the user named.
		if got != s {
			t.Errorf("ResolveBootc(%q) = %q, want passthrough unchanged", s, got)
		}
	})
}
