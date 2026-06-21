package catalog

import (
	"strings"
	"testing"
)

func TestFind_Found(t *testing.T) {
	img := Find("fedora")
	if img == nil {
		t.Fatal("Find(fedora) returned nil")
	}
	if img.Name != "fedora" {
		t.Errorf("expected Name=fedora, got %q", img.Name)
	}
	if img.DefaultUser != "fedora" {
		t.Errorf("expected DefaultUser=fedora, got %q", img.DefaultUser)
	}
	if img.ContainerDisk == "" {
		t.Error("ContainerDisk is empty")
	}
}

func TestFind_NotFound(t *testing.T) {
	if img := Find("nonexistent-os"); img != nil {
		t.Errorf("Find(nonexistent-os) returned %+v, expected nil", img)
	}
}

func TestFind_EmptyName(t *testing.T) {
	if img := Find(""); img != nil {
		t.Errorf("Find(\"\") returned %+v, expected nil", img)
	}
}

func TestCatalog_NotEmpty(t *testing.T) {
	if len(Images) == 0 {
		t.Fatal("Images catalog is empty")
	}
	for i, img := range Images {
		if img.Name == "" {
			t.Errorf("Images[%d] has empty Name", i)
		}
		// Exactly one boot source per entry.
		n := 0
		for _, s := range []string{img.ContainerDisk, img.URL, img.ISO} {
			if s != "" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("Images[%d] (%s) has %d of ContainerDisk/URL/ISO set, want exactly 1", i, img.Name, n)
		}
		// Desktop installer ISOs create the user interactively, so they have no
		// preset DefaultUser; cloud images and appliances must declare one.
		if img.DefaultUser == "" && img.Variant != "desktop" {
			t.Errorf("Images[%d] (%s) has empty DefaultUser", i, img.Name)
		}
		if img.Source == "" {
			t.Errorf("Images[%d] (%s) has empty Source", i, img.Name)
		}
	}
}

func TestCatalog_AllFindable(t *testing.T) {
	for _, img := range Images {
		found := Find(img.Name)
		if found == nil {
			t.Errorf("Find(%q) returned nil but image is in catalog", img.Name)
			continue
		}
		if found.Name != img.Name {
			t.Errorf("Find(%q).Name = %q, expected %q", img.Name, found.Name, img.Name)
		}
		if found.ContainerDisk != img.ContainerDisk {
			t.Errorf("Find(%q).ContainerDisk mismatch", img.Name)
		}
	}
}

func TestFind_CaseSensitive(t *testing.T) {
	// Names are case-sensitive — "Fedora" should not match "fedora"
	if img := Find("Fedora"); img != nil {
		t.Errorf("Find(Fedora) returned %+v, expected nil (case-sensitive)", img)
	}
}

func TestKind(t *testing.T) {
	cases := []struct {
		img  Image
		want string
	}{
		{Image{ContainerDisk: "quay.io/x"}, "containerDisk"},
		{Image{URL: "https://x/y.qcow2"}, "import"},
		{Image{ISO: "https://x/y.iso"}, "iso"},
	}
	for _, c := range cases {
		if got := c.img.Kind(); got != c.want {
			t.Errorf("Kind() = %q, want %q", got, c.want)
		}
	}
}

func TestCatalog_OfficialSources(t *testing.T) {
	// The reputable upstream sources requested for the catalog must be present.
	for _, name := range []string{
		"debian-12-official", "fedora-42-official",
		"centos-stream9-official", "almalinux-9-official", "turnkey-core",
	} {
		img := Find(name)
		if img == nil {
			t.Errorf("Find(%q) returned nil — official-source entry missing", name)
			continue
		}
		if img.ContainerDisk != "" {
			t.Errorf("%s: official-source entries must use URL/ISO, not ContainerDisk", name)
		}
	}
	// URL entries must point at the publisher's own domain over a real scheme.
	for _, img := range Images {
		for _, u := range []string{img.URL, img.ISO} {
			if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				t.Errorf("%s: source %q is not an http(s) URL", img.Name, u)
			}
		}
	}
}

func TestBootcCatalog(t *testing.T) {
	if len(BootcImages) == 0 {
		t.Fatal("BootcImages catalog is empty")
	}
	for i, b := range BootcImages {
		if b.Name == "" || b.Image == "" || b.Description == "" || b.Source == "" {
			t.Errorf("BootcImages[%d] (%s) has empty fields: %+v", i, b.Name, b)
		}
		if FindBootc(b.Name) == nil {
			t.Errorf("FindBootc(%q) returned nil but image is in catalog", b.Name)
		}
	}
}

func TestFindBootc_NotFound(t *testing.T) {
	if b := FindBootc("nonexistent"); b != nil {
		t.Errorf("FindBootc(nonexistent) = %+v, want nil", b)
	}
}

func TestResolveBootc(t *testing.T) {
	if got := ResolveBootc("fedora-bootc"); got != "quay.io/fedora/fedora-bootc:42" {
		t.Errorf("ResolveBootc(fedora-bootc) = %q", got)
	}
	// Non-catalog refs pass through.
	ref := "quay.io/example/custom-bootc:v1"
	if got := ResolveBootc(ref); got != ref {
		t.Errorf("ResolveBootc(%q) = %q, want passthrough", ref, got)
	}
}
