// Package sources persists user-defined catalog entries — custom container
// images, disk-image URLs, and installer ISOs — in a Kubernetes ConfigMap so
// they appear in the create wizard alongside the built-in catalog and survive
// web-pod restarts (the in-cluster pod has an ephemeral HOME). Everything shells
// to kubectl, consistent with the rest of Corral.
package sources

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/shell"
)

const (
	configMapName = "corral-sources"
	dataKey       = "sources.json"
)

// Source is a user-defined catalog entry — the same shape as catalog.Image,
// flagged Custom so the UI can mark and manage it.
type Source struct {
	catalog.Image
	Custom bool `json:"custom"`
}

var runner shell.Runner = shell.DefaultKubectl

// SetRunner overrides the command runner (for unit tests).
func SetRunner(r shell.Runner) { runner = r }

// Load returns the user-defined sources stored in the ConfigMap in ns. A
// missing ConfigMap (fresh cluster, or no kubectl) yields an empty list, not an
// error — callers degrade to the built-in catalog only.
func Load(ns string) ([]Source, error) {
	out, err := runner.Run("kubectl", "get", "configmap", configMapName, "-n", ns, "-o", "json")
	if err != nil {
		return nil, nil
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(out, &cm) != nil {
		return nil, nil
	}
	raw := cm.Data[dataKey]
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var srcs []Source
	if err := json.Unmarshal([]byte(raw), &srcs); err != nil {
		return nil, fmt.Errorf("parsing stored sources: %w", err)
	}
	for i := range srcs {
		srcs[i].Custom = true
	}
	return srcs, nil
}

// Add validates and stores a source, replacing any existing one with the same
// name (idempotent edit).
func Add(ns string, s Source) error {
	if err := validate(&s); err != nil {
		return err
	}
	cur, _ := Load(ns)
	out := make([]Source, 0, len(cur)+1)
	for _, e := range cur {
		if e.Name != s.Name {
			out = append(out, e)
		}
	}
	out = append(out, s)
	return save(ns, out)
}

// Remove deletes the named source. No-op if it doesn't exist.
func Remove(ns, name string) error {
	cur, _ := Load(ns)
	out := make([]Source, 0, len(cur))
	for _, e := range cur {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return save(ns, out)
}

func validate(s *Source) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	set := 0
	for _, v := range []string{s.ContainerDisk, s.URL, s.ISO} {
		if strings.TrimSpace(v) != "" {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("exactly one of container image, disk-image URL, or ISO URL must be set")
	}
	s.Custom = true
	if s.Source == "" {
		s.Source = "custom"
	}
	return nil
}

func save(ns string, srcs []Source) error {
	data, err := json.Marshal(srcs)
	if err != nil {
		return err
	}
	cm := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": configMapName, "namespace": ns},
		"data":       map[string]string{dataKey: string(data)},
	}
	manifest, err := json.Marshal(cm)
	if err != nil {
		return err
	}
	out, err := runner.RunStdin(string(manifest), "kubectl", "apply", "-f", "-")
	if err != nil {
		return fmt.Errorf("saving sources: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
