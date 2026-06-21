package kubevirt

import (
	"reflect"
	"testing"
)

func TestTagsFromLabels(t *testing.T) {
	labels := map[string]string{
		"corral.dev/template": "true",
		"corral.dev/tag.web":  "true",
		"corral.dev/tag.prod": "true",
		"corral.dev/tag.off":  "false", // not "true" → ignored
		"kubevirt.io/vm":      "x",
	}
	got := tagsFromLabels(labels)
	want := []string{"prod", "web"} // sorted, only the "true" tag.* labels
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tagsFromLabels = %v, want %v", got, want)
	}
}

func TestTagsFromLabels_None(t *testing.T) {
	if got := tagsFromLabels(map[string]string{"kubevirt.io/vm": "x"}); len(got) != 0 {
		t.Errorf("expected no tags, got %v", got)
	}
}

func TestSanitizeTag(t *testing.T) {
	cases := map[string]string{
		"Web":           "web",
		"  Prod  ":      "prod",
		"db-1":          "db-1",
		"has space!":    "hasspace",
		"-leading.dot-": "leading.dot",
		"":              "",
		"!!!":           "",
	}
	for in, want := range cases {
		if got := sanitizeTag(in); got != want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClient_SetTag(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"label", "vm", "testvm", "-n", "tailvm", "corral.dev/tag.web=true", "--overwrite"}, "", nil)
	if err := c.SetTag("testvm", "Web", true); err != nil {
		t.Fatalf("SetTag(on): %v", err)
	}
}

func TestClient_SetTag_Off(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"label", "vm", "testvm", "-n", "tailvm", "corral.dev/tag.web-", "--overwrite"}, "", nil)
	if err := c.SetTag("testvm", "web", false); err != nil {
		t.Fatalf("SetTag(off): %v", err)
	}
}

func TestClient_SetTag_Invalid(t *testing.T) {
	c, _ := newFakeClient()
	if err := c.SetTag("testvm", "!!!", true); err == nil {
		t.Error("expected error for invalid tag")
	}
}
