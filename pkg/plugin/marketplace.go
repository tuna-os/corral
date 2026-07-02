package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// DefaultMarketplaceURL is the curated plugin index. Override with
// CORRAL_MARKETPLACE_URL (e.g. a fork or a local file:// path isn't supported —
// use a raw https URL).
const DefaultMarketplaceURL = "https://raw.githubusercontent.com/tuna-os/corral/main/marketplace/index.json"

// Build is one platform's binary for a plugin.
type Build struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
}

// Entry is a marketplace plugin listing.
type Entry struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Version     string           `json:"version"`
	Homepage    string           `json:"homepage,omitempty"`
	Platforms   map[string]Build `json:"platforms"` // key: "linux/amd64"
}

// Index is the marketplace document.
type Index struct {
	Plugins []Entry `json:"plugins"`
}

func marketplaceURL() string {
	if u := os.Getenv("CORRAL_MARKETPLACE_URL"); u != "" {
		return u
	}
	return DefaultMarketplaceURL
}

// FetchIndex downloads and parses the marketplace index.
func FetchIndex() (*Index, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(marketplaceURL())
	if err != nil {
		return nil, fmt.Errorf("fetching marketplace: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("marketplace returned %s", resp.Status)
	}
	var idx Index
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return nil, fmt.Errorf("parsing marketplace: %w", err)
	}
	return &idx, nil
}

// Find returns the marketplace entry for a plugin name.
func (idx *Index) Find(name string) *Entry {
	for i := range idx.Plugins {
		if idx.Plugins[i].Name == name {
			return &idx.Plugins[i]
		}
	}
	return nil
}

func platformKey() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Install downloads the entry's binary for this platform into the plugin dir,
// verifying its checksum when one is published.
func (e *Entry) Install() error {
	b, ok := e.Platforms[platformKey()]
	if !ok {
		return fmt.Errorf("%s has no build for %s", e.Name, platformKey())
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(b.URL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", e.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", e.Name, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if b.SHA256 != "" {
		sum := hex.EncodeToString(sha256Sum(data))
		if sum != b.SHA256 {
			return fmt.Errorf("checksum mismatch for %s (got %s)", e.Name, sum)
		}
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	dst := filepath.Join(Dir(), "corral-"+e.Name)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return fmt.Errorf("installing %s: %w", e.Name, err)
	}
	return nil
}

// Remove deletes an installed plugin.
func Remove(name string) error {
	dst := filepath.Join(Dir(), "corral-"+name)
	if _, err := os.Stat(dst); err != nil {
		return fmt.Errorf("%s is not installed", name)
	}
	return os.Remove(dst)
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
