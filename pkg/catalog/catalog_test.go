package catalog

import (
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
	// Every entry must have a name and a containerDisk
	for i, img := range Images {
		if img.Name == "" {
			t.Errorf("Images[%d] has empty Name", i)
		}
		if img.ContainerDisk == "" {
			t.Errorf("Images[%d] (%s) has empty ContainerDisk", i, img.Name)
		}
		if img.DefaultUser == "" {
			t.Errorf("Images[%d] (%s) has empty DefaultUser", i, img.Name)
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
