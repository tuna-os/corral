package cmd

import (
	"testing"

	"github.com/tuna-os/corral/pkg/catalog"
)

func TestSearchBootcCatalog(t *testing.T) {
	if got := searchBootcCatalog(""); len(got) != len(catalog.BootcImages) {
		t.Errorf("empty term should return the full bootc catalog (%d), got %d", len(catalog.BootcImages), len(got))
	}
	got := searchBootcCatalog("fedora")
	if len(got) == 0 {
		t.Fatal("searchBootcCatalog(fedora) found nothing")
	}
	if got := searchBootcCatalog("definitely-not-a-bootc-image"); len(got) != 0 {
		t.Errorf("bogus term matched %d bootc images", len(got))
	}
}

func TestSearchCatalogs_All(t *testing.T) {
	os, bootc := searchCatalogs("", "")
	if len(os) != len(catalog.Images) {
		t.Errorf("type=\"\" should include the full OS catalog, got %d", len(os))
	}
	if len(bootc) != len(catalog.BootcImages) {
		t.Errorf("type=\"\" should include the full bootc catalog, got %d", len(bootc))
	}
}

func TestSearchCatalogs_OSOnly(t *testing.T) {
	os, bootc := searchCatalogs("", "os")
	if len(os) != len(catalog.Images) {
		t.Errorf("type=os should include the OS catalog, got %d", len(os))
	}
	if bootc != nil {
		t.Errorf("type=os should not touch the bootc catalog, got %d entries", len(bootc))
	}
}

func TestSearchCatalogs_BootcOnly(t *testing.T) {
	os, bootc := searchCatalogs("", "bootc")
	if os != nil {
		t.Errorf("type=bootc should not touch the OS catalog, got %d entries", len(os))
	}
	if len(bootc) != len(catalog.BootcImages) {
		t.Errorf("type=bootc should include the bootc catalog, got %d", len(bootc))
	}
}

func TestValidateImagesType(t *testing.T) {
	for _, ok := range []string{"", "os", "bootc"} {
		if err := validateImagesType(ok); err != nil {
			t.Errorf("validateImagesType(%q) should be valid, got %v", ok, err)
		}
	}
	if err := validateImagesType("banana"); err == nil {
		t.Error("validateImagesType(\"banana\") should error, not silently fall through")
	}
}
