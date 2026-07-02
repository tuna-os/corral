package cmd

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/catalog"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestSearchCatalog(t *testing.T) {
	if got := searchCatalog(""); len(got) != len(catalog.Images) {
		t.Errorf("empty term should return the full catalog (%d), got %d", len(catalog.Images), len(got))
	}
	got := searchCatalog("fedora")
	if len(got) == 0 {
		t.Fatal("searchCatalog(fedora) found nothing")
	}
	for _, img := range got {
		hay := strings.ToLower(img.Name + " " + img.Description)
		if !strings.Contains(hay, "fedora") {
			t.Errorf("match %q does not contain the term", img.Name)
		}
	}
	if got := searchCatalog("definitely-not-an-os"); len(got) != 0 {
		t.Errorf("bogus term matched %d images", len(got))
	}
}

func TestPullTemplate(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	fake.AddPrefixResponse("kubectl label vm tmpl-fedora -n corral-vms corral.dev/template=true", "labeled", nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	kubevirt.SetDefaultRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
		kubevirt.SetDefaultRunner(nil)
	})
	t.Setenv("HOME", t.TempDir())

	if err := pullTemplate("fedora", ""); err != nil {
		t.Fatalf("pullTemplate: %v", err)
	}
	var labeled bool
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "label" {
			labeled = true
		}
	}
	if !labeled {
		t.Error("pulled image was not marked as a template")
	}
}

func TestPullTemplate_UnknownImage(t *testing.T) {
	if err := pullTemplate("not-an-image", ""); err == nil {
		t.Fatal("unknown catalog image should fail")
	}
}

func TestTemplateName(t *testing.T) {
	if got := templateName("ubuntu"); got != "tmpl-ubuntu" {
		t.Errorf("templateName = %q", got)
	}
}
