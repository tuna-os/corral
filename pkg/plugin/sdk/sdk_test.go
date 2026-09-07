package sdk

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func captureStdout(t *testing.T, fn func() bool) (bool, string) {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	result := fn()

	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	return result, buf.String()
}

func TestHandleMetadata_NoArgs(t *testing.T) {
	withArgs(t, []string{"corral-test"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "test"})
		})
		if ok {
			t.Fatal("expected HandleMetadata to return false with no flag")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleMetadata_TooManyArgs(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata", "extra"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "test"})
		})
		if ok {
			t.Fatal("expected HandleMetadata to return false with extra args")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleMetadata_WrongFlag(t *testing.T) {
	withArgs(t, []string{"corral-test", "--help"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "test"})
		})
		if ok {
			t.Fatal("expected HandleMetadata to return false for unrelated flag")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleMetadata_DefaultsAPI(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{
				Name:              "bootc",
				Version:           "0.3.0",
				Description:       "Build bootable container images into VMs",
				Capabilities:      []string{"cli-command", "backend-workflow"},
				SupportedBackends: []string{"kubevirt"},
			})
		})
		if !ok {
			t.Fatal("expected HandleMetadata to return true for the metadata flag")
		}

		var got Metadata
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not valid JSON metadata: %v\noutput: %s", err, out)
		}
		if got.API != API {
			t.Errorf("API = %q, want default %q", got.API, API)
		}
		if got.Name != "bootc" {
			t.Errorf("Name = %q, want %q", got.Name, "bootc")
		}
		if len(got.Capabilities) != 2 {
			t.Errorf("Capabilities = %v, want 2 entries", got.Capabilities)
		}
	})
}

func TestHandleMetadata_PreservesExplicitAPI(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{API: "corral.plugin/v2", Name: "custom"})
		})
		if !ok {
			t.Fatal("expected HandleMetadata to return true")
		}
		var got Metadata
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not valid JSON metadata: %v", err)
		}
		if got.API != "corral.plugin/v2" {
			t.Errorf("API = %q, want explicit %q to be preserved", got.API, "corral.plugin/v2")
		}
	})
}

func TestHandleMetadata_OmitsEmptyOptionalFields(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata"}, func() {
		_, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "minimal", Version: "0.0.1"})
		})
		var raw map[string]any
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		for _, field := range []string{"description", "capabilities", "permissions", "supportedBackends"} {
			if _, present := raw[field]; present {
				t.Errorf("expected omitempty field %q to be absent, got %v", field, raw[field])
			}
		}
	})
}
