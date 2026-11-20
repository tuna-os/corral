package main

import (
	"path/filepath"
	"testing"
)

func TestRevocationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revoked.json")
	g := &gateway{}
	if err := g.useRevokeFile(path); err != nil {
		t.Fatal(err)
	}
	g.revoke("sid-1")

	restarted := &gateway{}
	if err := restarted.useRevokeFile(path); err != nil {
		t.Fatal(err)
	}
	if !restarted.isRevoked("sid-1") {
		t.Fatal("revoked session accepted after restart")
	}
	if restarted.isRevoked("sid-2") {
		t.Fatal("unrelated session refused")
	}
}
