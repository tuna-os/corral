package catalog

// BootcImage is a curated bootc (bootable container) image. These are the
// upstream-maintained bases the bootc plugin can build into VM disks:
// Fedora/CentOS bootc from the distros themselves, Universal Blue's uCore
// server images, and the Universal Blue desktop images (Bluefin/Aurora/
// Bazzite). All refs verified against their registries.
type BootcImage struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"` // OCI ref
	Source      string `json:"source"`
	Logo        string `json:"logo"` // simple-icons slug for the wizard card
}

// bootcBases are the upstream distro bootc bases (hand-maintained; stable refs).
var bootcBases = []BootcImage{
	{"fedora-bootc", "Fedora bootc 42 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:42", "fedoraproject.org", "fedora"},
	{"fedora-bootc-43", "Fedora bootc 43 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:43", "fedoraproject.org", "fedora"},
	{"fedora-bootc-44", "Fedora bootc 44 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:44", "fedoraproject.org", "fedora"},
	{"centos-bootc-stream9", "CentOS Stream 9 bootc — official CentOS bootable container base", "quay.io/centos-bootc/centos-bootc:stream9", "centos.org", "centos"},
	{"centos-bootc-stream10", "CentOS Stream 10 bootc — official CentOS bootable container base", "quay.io/centos-bootc/centos-bootc:stream10", "centos.org", "centos"},
}

// BootcImages is the full bootc catalog: the distro bases plus the
// freshness-filtered Universal Blue / Project Bluefin / TunaOS images generated
// by scripts/regen-catalog.py (see catalog_generated.go). Regenerate the
// dynamic set with `just regen-catalog`.
var BootcImages = append(append([]BootcImage{}, bootcBases...), generatedBootcImages...)

// FindBootc returns the bootc catalog image with the given name, or nil.
func FindBootc(name string) *BootcImage {
	for i := range BootcImages {
		if BootcImages[i].Name == name {
			return &BootcImages[i]
		}
	}
	return nil
}

// ResolveBootc maps a bootc catalog name to its OCI ref; anything that isn't
// a catalog name (already an image ref) passes through unchanged.
func ResolveBootc(nameOrRef string) string {
	if b := FindBootc(nameOrRef); b != nil {
		return b.Image
	}
	return nameOrRef
}
