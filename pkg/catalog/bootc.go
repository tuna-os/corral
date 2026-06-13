package catalog

// BootcImage is a curated bootc (bootable container) image. These are the
// upstream-maintained bases the bootc plugin can build into VM disks:
// Fedora/CentOS bootc from the distros themselves, and Universal Blue's
// uCore server images. All refs verified against their registries.
type BootcImage struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"` // OCI ref
	Source      string `json:"source"`
	Logo        string `json:"logo"` // simple-icons slug for the wizard card
}

// BootcImages is the curated bootc catalog (ships with the bootc plugin).
var BootcImages = []BootcImage{
	{"fedora-bootc", "Fedora bootc 44 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:44", "fedoraproject.org", "fedora"},
	{"fedora-bootc-43", "Fedora bootc 43 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:43", "fedoraproject.org", "fedora"},
	{"centos-bootc-stream9", "CentOS Stream 9 bootc — official CentOS bootable container base", "quay.io/centos-bootc/centos-bootc:stream9", "centos.org", "centos"},
	{"centos-bootc-stream10", "CentOS Stream 10 bootc — official CentOS bootable container base", "quay.io/centos-bootc/centos-bootc:stream10", "centos.org", "centos"},
	{"ucore", "Universal Blue uCore stable — Fedora CoreOS server with batteries included", "ghcr.io/ublue-os/ucore:stable", "universal-blue.org", "fedora"},
	{"ucore-minimal", "Universal Blue uCore minimal stable — lean Fedora CoreOS server", "ghcr.io/ublue-os/ucore-minimal:stable", "universal-blue.org", "fedora"},
}

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
