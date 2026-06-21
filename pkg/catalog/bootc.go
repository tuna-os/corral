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

// BootcImages is the curated bootc catalog (ships with the bootc plugin). The
// Universal Blue / Project Bluefin / TunaOS entries are freshness-filtered:
// only images with a tag built within ~60 days are listed (regenerated from the
// ghcr.io registries; stale variants are dropped).
var BootcImages = []BootcImage{
	// ── Distro bootc bases ──
	{"fedora-bootc", "Fedora bootc 42 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:42", "fedoraproject.org", "fedora"},
	{"fedora-bootc-43", "Fedora bootc 43 — official Fedora bootable container base", "quay.io/fedora/fedora-bootc:43", "fedoraproject.org", "fedora"},
	{"centos-bootc-stream9", "CentOS Stream 9 bootc — official CentOS bootable container base", "quay.io/centos-bootc/centos-bootc:stream9", "centos.org", "centos"},
	{"centos-bootc-stream10", "CentOS Stream 10 bootc — official CentOS bootable container base", "quay.io/centos-bootc/centos-bootc:stream10", "centos.org", "centos"},
	// ── Universal Blue (ublue-os) — fresh desktops + servers ──
	{"bluefin", "Universal Blue Bluefin — GNOME bootc desktop", "ghcr.io/ublue-os/bluefin:stable", "projectbluefin.io", "gnome"},
	{"bluefin-dx", "Bluefin DX — GNOME desktop + developer tooling", "ghcr.io/ublue-os/bluefin-dx:stable", "projectbluefin.io", "gnome"},
	{"bluefin-gdx", "Bluefin GDX — GNOME dev desktop (GPU/AI tooling)", "ghcr.io/ublue-os/bluefin-gdx:stable", "projectbluefin.io", "gnome"},
	{"bluefin-nvidia-open", "Bluefin — GNOME desktop, NVIDIA open driver", "ghcr.io/ublue-os/bluefin-nvidia-open:stable", "projectbluefin.io", "nvidia"},
	{"bluefin-dx-nvidia-open", "Bluefin DX — dev desktop, NVIDIA open driver", "ghcr.io/ublue-os/bluefin-dx-nvidia-open:stable", "projectbluefin.io", "nvidia"},
	{"aurora", "Universal Blue Aurora — KDE Plasma bootc desktop", "ghcr.io/ublue-os/aurora:stable", "getaurora.dev", "kde"},
	{"aurora-dx", "Aurora DX — KDE Plasma + developer tooling", "ghcr.io/ublue-os/aurora-dx:stable", "getaurora.dev", "kde"},
	{"aurora-nvidia-open", "Aurora — KDE Plasma, NVIDIA open driver", "ghcr.io/ublue-os/aurora-nvidia-open:stable", "getaurora.dev", "nvidia"},
	{"aurora-dx-nvidia-open", "Aurora DX — KDE dev, NVIDIA open driver", "ghcr.io/ublue-os/aurora-dx-nvidia-open:stable", "getaurora.dev", "nvidia"},
	{"bazzite", "Bazzite — gaming bootc desktop (KDE)", "ghcr.io/ublue-os/bazzite:stable", "bazzite.gg", "steam"},
	{"bazzite-gnome", "Bazzite — gaming bootc desktop (GNOME)", "ghcr.io/ublue-os/bazzite-gnome:stable", "bazzite.gg", "steam"},
	{"bazzite-dx", "Bazzite DX — gaming + developer tooling (KDE)", "ghcr.io/ublue-os/bazzite-dx:stable", "bazzite.gg", "steam"},
	{"bazzite-dx-gnome", "Bazzite DX — gaming + dev tooling (GNOME)", "ghcr.io/ublue-os/bazzite-dx-gnome:stable", "bazzite.gg", "steam"},
	{"bazzite-deck", "Bazzite Deck — handheld/Steam Deck image (KDE)", "ghcr.io/ublue-os/bazzite-deck:stable", "bazzite.gg", "steam"},
	{"bazzite-deck-gnome", "Bazzite Deck — handheld image (GNOME)", "ghcr.io/ublue-os/bazzite-deck-gnome:stable", "bazzite.gg", "steam"},
	{"bazzite-nvidia", "Bazzite — gaming desktop, NVIDIA driver", "ghcr.io/ublue-os/bazzite-nvidia:stable", "bazzite.gg", "nvidia"},
	{"bazzite-nvidia-open", "Bazzite — gaming desktop, NVIDIA open driver", "ghcr.io/ublue-os/bazzite-nvidia-open:stable", "bazzite.gg", "nvidia"},
	{"kinoite-main", "Universal Blue Kinoite — KDE Fedora Atomic base", "ghcr.io/ublue-os/kinoite-main:latest", "universal-blue.org", "kde"},
	{"silverblue-main", "Universal Blue Silverblue — GNOME Fedora Atomic base", "ghcr.io/ublue-os/silverblue-main:latest", "universal-blue.org", "gnome"},
	{"steambox", "SteamBox — Steam Big Picture gaming image (KDE)", "ghcr.io/ublue-os/steambox:latest", "universal-blue.org", "steam"},
	{"steambox-gnome", "SteamBox — Steam Big Picture gaming image (GNOME)", "ghcr.io/ublue-os/steambox-gnome:latest", "universal-blue.org", "steam"},
	{"ucore", "Universal Blue uCore — Fedora CoreOS server, batteries included", "ghcr.io/ublue-os/ucore:stable", "universal-blue.org", "fedora"},
	{"ucore-minimal", "uCore minimal — lean Fedora CoreOS server", "ghcr.io/ublue-os/ucore-minimal:stable", "universal-blue.org", "fedora"},
	{"ucore-hci", "uCore HCI — hyper-converged (libvirt/KVM) server", "ghcr.io/ublue-os/ucore-hci:stable", "universal-blue.org", "fedora"},
	// ── Project Bluefin (projectbluefin) — Dakota + LTS ──
	{"dakota", "Bluefin Dakota — next-gen Bluefin (projectbluefin)", "ghcr.io/projectbluefin/dakota:latest", "projectbluefin.io", "gnome"},
	{"dakota-nvidia", "Bluefin Dakota — NVIDIA driver", "ghcr.io/projectbluefin/dakota-nvidia:latest", "projectbluefin.io", "nvidia"},
	{"bluefin-lts", "Bluefin LTS — CentOS-based long-term Bluefin", "ghcr.io/projectbluefin/bluefin-lts:latest", "projectbluefin.io", "gnome"},
	{"bluefin-lts-hwe", "Bluefin LTS HWE — hardware-enablement kernel", "ghcr.io/projectbluefin/bluefin-lts-hwe:latest", "projectbluefin.io", "gnome"},
	{"bluefin-lts-hwe-nvidia", "Bluefin LTS HWE — NVIDIA driver", "ghcr.io/projectbluefin/bluefin-lts-hwe-nvidia:latest", "projectbluefin.io", "nvidia"},
	// ── TunaOS (tuna-os) ──
	{"tuna-albacore", "TunaOS Albacore — bootc image", "ghcr.io/tuna-os/albacore:latest", "tunaos.org", "linux"},
	{"tuna-bonito", "TunaOS Bonito — bootc image", "ghcr.io/tuna-os/bonito:latest", "tunaos.org", "linux"},
	{"tuna-chunkah", "TunaOS Chunkah — bootc image", "ghcr.io/tuna-os/chunkah:latest", "tunaos.org", "linux"},
	{"tuna-grouper", "TunaOS Grouper — bootc image", "ghcr.io/tuna-os/grouper:latest", "tunaos.org", "linux"},
	{"tuna-skipjack", "TunaOS Skipjack — bootc image", "ghcr.io/tuna-os/skipjack:latest", "tunaos.org", "linux"},
	{"tuna-tacklebox", "TunaOS Tacklebox — bootc image", "ghcr.io/tuna-os/tacklebox:latest", "tunaos.org", "linux"},
	{"tuna-tavern", "TunaOS Tavern — bootc image", "ghcr.io/tuna-os/tavern:latest", "tunaos.org", "linux"},
	{"tuna-yellowfin", "TunaOS Yellowfin — bootc image", "ghcr.io/tuna-os/yellowfin:latest", "tunaos.org", "linux"},
	{"tuna-arch-bootc", "TunaOS Fisherman — Arch bootc base", "ghcr.io/tuna-os/fisherman/arch-bootc:latest", "tunaos.org", "archlinux"},
	{"tuna-centos-bootc", "TunaOS Fisherman — CentOS bootc base", "ghcr.io/tuna-os/fisherman/centos-bootc:latest", "tunaos.org", "centos"},
	{"tuna-debian-bootc", "TunaOS Fisherman — Debian bootc base", "ghcr.io/tuna-os/fisherman/debian-bootc:latest", "tunaos.org", "debian"},
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
