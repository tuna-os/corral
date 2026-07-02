package sources

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestLoad_NoConfigMap(t *testing.T) {
	r := shell.NewFake() // get configmap → error (unregistered)
	SetRunner(r)
	defer SetRunner(shell.Real{})
	got, err := Load("corral-vms")
	if err != nil || got != nil {
		t.Errorf("missing ConfigMap should be empty+nil, got %v / %v", got, err)
	}
}

func TestLoad_ParsesAndFlagsCustom(t *testing.T) {
	r := shell.NewFake()
	cm := `{"data":{"sources.json":"[{\"name\":\"myimg\",\"containerDisk\":\"ghcr.io/me/img:1\"}]"}}`
	r.AddResponseKV("kubectl", []string{"get", "configmap", "corral-sources", "-n", "corral-vms", "-o", "json"}, cm, nil)
	SetRunner(r)
	defer SetRunner(shell.Real{})

	got, err := Load("corral-vms")
	if err != nil || len(got) != 1 {
		t.Fatalf("Load = %v / %v", got, err)
	}
	if got[0].Name != "myimg" || got[0].ContainerDisk != "ghcr.io/me/img:1" || !got[0].Custom {
		t.Errorf("unexpected source: %+v", got[0])
	}
}

func TestValidate(t *testing.T) {
	bad := []Source{
		{Image: catalog.Image{Name: ""}},                                          // no name
		{Image: catalog.Image{Name: "x"}},                                         // no uri
		{Image: catalog.Image{Name: "x", ContainerDisk: "a", URL: "b"}},           // two set
		{Image: catalog.Image{Name: "x", ContainerDisk: "a", URL: "b", ISO: "c"}}, // three
	}
	for i, s := range bad {
		s := s
		if err := validate(&s); err == nil {
			t.Errorf("case %d should be invalid: %+v", i, s)
		}
	}
	ok := Source{Image: catalog.Image{Name: "good", ISO: "https://x/y.iso"}}
	if err := validate(&ok); err != nil {
		t.Errorf("valid source rejected: %v", err)
	}
	if !ok.Custom || ok.Source != "custom" {
		t.Errorf("validate should set Custom + default Source: %+v", ok)
	}
}

func TestAdd_ReplacesByName_AndSaves(t *testing.T) {
	r := shell.NewFake()
	// Load (existing has "keep" + an old "myimg") → both present.
	existing := `{"data":{"sources.json":"[{\"name\":\"keep\",\"iso\":\"https://x/a.iso\"},{\"name\":\"myimg\",\"containerDisk\":\"old\"}]"}}`
	r.AddResponseKV("kubectl", []string{"get", "configmap", "corral-sources", "-n", "corral-vms", "-o", "json"}, existing, nil)
	r.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "configmap/corral-sources configured", nil)
	SetRunner(r)
	defer SetRunner(shell.Real{})

	err := Add("corral-vms", Source{Image: catalog.Image{Name: "myimg", ContainerDisk: "ghcr.io/me/new:2"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Inspect the applied ConfigMap: "keep" survives, "myimg" is replaced (not duplicated).
	var applied string
	for _, c := range r.Calls() {
		if len(c.Args) >= 2 && c.Args[0] == "apply" {
			applied = c.Stdin
		}
	}
	if applied == "" {
		t.Fatal("no apply call captured")
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(applied), &cm); err != nil {
		t.Fatalf("applied manifest not JSON: %v", err)
	}
	var saved []Source
	json.Unmarshal([]byte(cm.Data["sources.json"]), &saved)
	if len(saved) != 2 {
		t.Fatalf("expected 2 sources after replace, got %d: %+v", len(saved), saved)
	}
	for _, s := range saved {
		if s.Name == "myimg" && s.ContainerDisk != "ghcr.io/me/new:2" {
			t.Errorf("myimg not replaced: %+v", s)
		}
	}
}

func TestRemove(t *testing.T) {
	r := shell.NewFake()
	existing := `{"data":{"sources.json":"[{\"name\":\"a\",\"iso\":\"https://x/a.iso\"},{\"name\":\"b\",\"iso\":\"https://x/b.iso\"}]"}}`
	r.AddResponseKV("kubectl", []string{"get", "configmap", "corral-sources", "-n", "corral-vms", "-o", "json"}, existing, nil)
	r.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "ok", nil)
	SetRunner(r)
	defer SetRunner(shell.Real{})

	if err := Remove("corral-vms", "a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	var applied string
	for _, c := range r.Calls() {
		if len(c.Args) >= 2 && c.Args[0] == "apply" {
			applied = c.Stdin
		}
	}
	if strings.Contains(applied, `\"name\":\"a\"`) || !strings.Contains(applied, "b") {
		t.Errorf("Remove should drop 'a' and keep 'b': %s", applied)
	}
}
