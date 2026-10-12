package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

func TestTheme_GetAndPut_StandardMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	activeTheme = themeDefaults
	cliTheme = ThemeConfig{}

	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}

	// Initial GET should return defaults
	req := httptest.NewRequest(http.MethodGet, "/api/theme", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/theme: expected 200, got %d", rec.Code)
	}
	var theme ThemeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &theme); err != nil {
		t.Fatalf("decode theme: %v", err)
	}
	if theme.Accent != themeDefaults.Accent || theme.BrandTitle != themeDefaults.BrandTitle {
		t.Errorf("expected default theme, got %+v", theme)
	}

	// PUT should update active theme and persist to config.yaml
	putBody := `{"accent":"#10b981","brand_title":"CustomBrand","brand_subtitle":"Staging"}`
	req = httptest.NewRequest(http.MethodPut, "/api/theme", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/theme: expected 200, got %d", rec.Code)
	}
	var updated ThemeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated theme: %v", err)
	}
	if updated.Accent != "#10b981" || updated.BrandTitle != "CustomBrand" || updated.BrandSubtitle != "Staging" {
		t.Errorf("updated theme mismatch: %+v", updated)
	}

	// Verify it was saved to config.yaml
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Web.Accent != "#10b981" || cfg.Web.BrandTitle != "CustomBrand" || cfg.Web.BrandSubtitle != "Staging" {
		t.Errorf("config.yaml not updated properly: %+v", cfg.Web)
	}
}

func TestTheme_Put_DemoMode_MemoryOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	activeTheme = themeDefaults
	cliTheme = ThemeConfig{}
	prevDemo := demoMode
	// Only the theme handler's demo switch is under test. EnableDemo would
	// also swap every backend seam for the fake cluster and leak it into
	// later tests; newDemoServer is the helper that restores those.
	demoMode = true
	t.Cleanup(func() {
		demoMode = prevDemo
		activeTheme = themeDefaults
		cliTheme = ThemeConfig{}
	})

	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}

	cfgPath := config.DefaultPath()
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("expected config file %q not to exist before PUT", cfgPath)
	}

	putBody := `{"accent":"#22c55e","brand_title":"SmokeTest"}`
	req := httptest.NewRequest(http.MethodPut, "/api/theme", bytes.NewBufferString(putBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/theme: expected 200, got %d", rec.Code)
	}

	var theme ThemeConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &theme); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if theme.Accent != "#22c55e" || theme.BrandTitle != "SmokeTest" {
		t.Errorf("theme not updated in memory: %+v", theme)
	}

	// Config file must NOT exist on disk
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("demo PUT /api/theme wrote config to %s", cfgPath)
	}
}
