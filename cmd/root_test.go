package cmd

import (
	"testing"
)

func TestPersistentPreRunE_InitializesRegistryStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registryStore = nil

	if err := rootCmd.PersistentPreRunE(rootCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}
	if registryStore == nil {
		t.Error("expected PersistentPreRunE to set registryStore")
	}
}

func TestRootCmd_Metadata(t *testing.T) {
	if rootCmd.Use != "corral" {
		t.Errorf("Use = %q, want corral", rootCmd.Use)
	}
	if rootCmd.Short == "" {
		t.Error("expected a non-empty Short description")
	}
}
