package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestHandleAddSource_PersistsAndValidates(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	// No ConfigMap yet (Load → error → empty), and apply succeeds.
	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "configmap/corral-sources created", nil)

	// Valid add.
	resp, err := http.Post(fx.Server.URL+"/api/sources", "application/json",
		strings.NewReader(`{"name":"myiso","kind":"iso","uri":"https://x/y.iso","description":"d"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("add: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The applied ConfigMap carries the source as iso.
	var applied string
	for _, c := range fx.Runner.Calls() {
		if len(c.Args) >= 2 && c.Args[0] == "apply" {
			applied = c.Stdin
		}
	}
	if !strings.Contains(applied, "myiso") || !strings.Contains(applied, "https://x/y.iso") {
		t.Errorf("applied ConfigMap missing the source: %s", applied)
	}

	// Invalid (no uri) → 400.
	bad, _ := http.Post(fx.Server.URL+"/api/sources", "application/json",
		strings.NewReader(`{"name":"x","kind":"iso","uri":""}`))
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("missing uri should be 400, got %d", bad.StatusCode)
	}
	bad.Body.Close()
}

func TestHandleListSources(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()
	fx.Runner.AddResponseKV("kubectl", []string{"get", "configmap", "corral-sources", "-n", "corral-vms", "-o", "json"},
		`{"data":{"sources.json":"[{\"name\":\"a\",\"url\":\"https://x/a.qcow2\"}]"}}`, nil)

	resp, err := http.Get(fx.Server.URL + "/api/sources")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != 1 || got[0]["name"] != "a" || got[0]["custom"] != true {
		t.Errorf("unexpected sources list: %v", got)
	}
}

func TestHandleImages_IncludesCustom(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()
	fx.Runner.AddResponseKV("kubectl", []string{"get", "configmap", "corral-sources", "-n", "corral-vms", "-o", "json"},
		`{"data":{"sources.json":"[{\"name\":\"mine\",\"containerDisk\":\"ghcr.io/me/x:1\"}]"}}`, nil)

	resp, err := http.Get(fx.Server.URL + "/api/images")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var imgs []map[string]any
	json.NewDecoder(resp.Body).Decode(&imgs)
	var foundCatalog, foundCustom bool
	for _, i := range imgs {
		if i["custom"] == true && i["name"] == "mine" {
			foundCustom = true
		}
		if i["name"] == "fedora" {
			foundCatalog = true
		}
	}
	if !foundCatalog || !foundCustom {
		t.Errorf("merged catalog should have both built-ins and the custom source (catalog=%v custom=%v)", foundCatalog, foundCustom)
	}
}
