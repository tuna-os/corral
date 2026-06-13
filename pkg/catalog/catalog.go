// Package catalog is Corral's built-in list of ready-to-boot OS images, drawn
// only from reputable publishers: the kubevirt-maintained quay.io/containerdisks
// org, and the distros' own download servers (cloud.debian.org,
// fedoraproject.org, cloud.centos.org, repo.almalinux.org) plus TurnKey Linux
// appliance ISOs. Three entry kinds:
//
//   - ContainerDisk — a KubeVirt containerdisk (cloud image packaged as an OCI
//     image, cloud-init enabled). Boots directly, no import step.
//   - URL — a qcow2/raw cloud image straight from the distro's own mirror,
//     imported by CDI on create.
//   - ISO — an installer ISO (TurnKey appliances); CDI imports it and the VM
//     boots the installer with a blank disk. Finish the install over VNC.
//
// Arbitrary images can still be brought in with `corral create --import <url>`.
package catalog

// Image is a catalog entry. Exactly one of ContainerDisk, URL, or ISO is set.
type Image struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContainerDisk string `json:"containerDisk,omitempty"` // OCI containerdisk ref (boots directly)
	URL           string `json:"url,omitempty"`           // qcow2/raw cloud image (CDI import)
	ISO           string `json:"iso,omitempty"`           // installer ISO (CDI import, blank disk)
	DefaultUser   string `json:"defaultUser"`
	Source        string `json:"source"`  // publisher, e.g. "quay.io/containerdisks", "debian.org"
	Logo          string `json:"logo"`    // simple-icons slug for the wizard card (letter badge fallback)
	Variant       string `json:"variant"` // "server", "desktop", or "appliance" — wizard filter
}

// Kind reports how the entry boots: "containerDisk", "import", or "iso".
func (i *Image) Kind() string {
	switch {
	case i.ContainerDisk != "":
		return "containerDisk"
	case i.URL != "":
		return "import"
	default:
		return "iso"
	}
}

// Images is the curated catalog.
var Images = []Image{
	// KubeVirt containerdisks — boot directly, no import step.
	{Name: "fedora", Description: "Fedora 42 cloud", ContainerDisk: "quay.io/containerdisks/fedora:42", DefaultUser: "fedora", Source: "quay.io/containerdisks", Logo: "fedora", Variant: "server"},
	{Name: "ubuntu", Description: "Ubuntu 24.04 LTS cloud", ContainerDisk: "quay.io/containerdisks/ubuntu:24.04", DefaultUser: "ubuntu", Source: "quay.io/containerdisks", Logo: "ubuntu", Variant: "server"},
	{Name: "ubuntu-22.04", Description: "Ubuntu 22.04 LTS cloud", ContainerDisk: "quay.io/containerdisks/ubuntu:22.04", DefaultUser: "ubuntu", Source: "quay.io/containerdisks", Logo: "ubuntu", Variant: "server"},
	{Name: "debian", Description: "Debian 12 (Bookworm) cloud", ContainerDisk: "quay.io/containerdisks/debian:12", DefaultUser: "debian", Source: "quay.io/containerdisks", Logo: "debian", Variant: "server"},
	{Name: "centos-stream9", Description: "CentOS Stream 9", ContainerDisk: "quay.io/containerdisks/centos-stream:9", DefaultUser: "cloud-user", Source: "quay.io/containerdisks", Logo: "centos", Variant: "server"},
	{Name: "rocky", Description: "Rocky Linux 9", ContainerDisk: "quay.io/containerdisks/rockylinux:9", DefaultUser: "cloud-user", Source: "quay.io/containerdisks", Logo: "rockylinux", Variant: "server"},
	{Name: "almalinux", Description: "AlmaLinux 9", ContainerDisk: "quay.io/containerdisks/almalinux:9", DefaultUser: "cloud-user", Source: "quay.io/containerdisks", Logo: "almalinux", Variant: "server"},
	{Name: "opensuse-leap", Description: "openSUSE Leap 15.6", ContainerDisk: "quay.io/containerdisks/opensuse-leap:15.6", DefaultUser: "opensuse", Source: "quay.io/containerdisks", Logo: "opensuse", Variant: "server"},

	// Official distro cloud images, fetched from the distros' own mirrors
	// (CDI import — VM gets a real PVC disk, survives restarts with state).
	{Name: "debian-12-official", Description: "Debian 12 (Bookworm) genericcloud, from cloud.debian.org",
		URL: "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2", DefaultUser: "debian", Source: "debian.org", Logo: "debian", Variant: "server"},
	{Name: "debian-13-official", Description: "Debian 13 (Trixie) genericcloud, from cloud.debian.org",
		URL: "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2", DefaultUser: "debian", Source: "debian.org", Logo: "debian", Variant: "server"},
	{Name: "fedora-42-official", Description: "Fedora 42 Cloud Base, from fedoraproject.org",
		URL: "https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2", DefaultUser: "fedora", Source: "fedoraproject.org", Logo: "fedora", Variant: "server"},
	{Name: "centos-stream9-official", Description: "CentOS Stream 9 GenericCloud, from cloud.centos.org",
		URL: "https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2", DefaultUser: "cloud-user", Source: "centos.org", Logo: "centos", Variant: "server"},
	{Name: "centos-stream10-official", Description: "CentOS Stream 10 GenericCloud, from cloud.centos.org",
		URL: "https://cloud.centos.org/centos/10-stream/x86_64/images/CentOS-Stream-GenericCloud-10-latest.x86_64.qcow2", DefaultUser: "cloud-user", Source: "centos.org", Logo: "centos", Variant: "server"},
	{Name: "almalinux-9-official", Description: "AlmaLinux 9 GenericCloud, from repo.almalinux.org",
		URL: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2", DefaultUser: "almalinux", Source: "almalinux.org", Logo: "almalinux", Variant: "server"},
	{Name: "almalinux-10-official", Description: "AlmaLinux 10 GenericCloud, from repo.almalinux.org",
		URL: "https://repo.almalinux.org/almalinux/10/cloud/x86_64/images/AlmaLinux-10-GenericCloud-latest.x86_64.qcow2", DefaultUser: "almalinux", Source: "almalinux.org", Logo: "almalinux", Variant: "server"},

	// TurnKey Linux appliances — installer ISOs (finish the install over VNC).
	// TurnKey's mirror serves plain HTTP only; releases are GPG-signed upstream.
	{Name: "turnkey-core", Description: "TurnKey Core 18.1 — minimal Debian appliance base (installer ISO)",
		ISO: "http://mirror.turnkeylinux.org/turnkeylinux/images/iso/turnkey-core-18.1-bookworm-amd64.iso", DefaultUser: "root", Source: "turnkeylinux.org", Logo: "", Variant: "appliance"},
	{Name: "turnkey-lamp", Description: "TurnKey LAMP 18.1 — Apache/MySQL/PHP stack (installer ISO)",
		ISO: "http://mirror.turnkeylinux.org/turnkeylinux/images/iso/turnkey-lamp-18.1-bookworm-amd64.iso", DefaultUser: "root", Source: "turnkeylinux.org", Logo: "", Variant: "appliance"},
	{Name: "turnkey-wordpress", Description: "TurnKey WordPress 18.1 (installer ISO)",
		ISO: "http://mirror.turnkeylinux.org/turnkeylinux/images/iso/turnkey-wordpress-18.1-bookworm-amd64.iso", DefaultUser: "root", Source: "turnkeylinux.org", Logo: "wordpress", Variant: "appliance"},
	{Name: "turnkey-nextcloud", Description: "TurnKey Nextcloud 18.1 (installer ISO)",
		ISO: "http://mirror.turnkeylinux.org/turnkeylinux/images/iso/turnkey-nextcloud-18.1-bookworm-amd64.iso", DefaultUser: "root", Source: "turnkeylinux.org", Logo: "nextcloud", Variant: "appliance"},
	{Name: "turnkey-gitlab", Description: "TurnKey GitLab 18.1 (installer ISO)",
		ISO: "http://mirror.turnkeylinux.org/turnkeylinux/images/iso/turnkey-gitlab-18.1-bookworm-amd64.iso", DefaultUser: "root", Source: "turnkeylinux.org", Logo: "gitlab", Variant: "appliance"},
	{Name: "turnkey-fileserver", Description: "TurnKey File Server 18.1 — Samba/WebDAV NAS (installer ISO)",
		ISO: "http://mirror.turnkeylinux.org/turnkeylinux/images/iso/turnkey-fileserver-18.1-bookworm-amd64.iso", DefaultUser: "root", Source: "turnkeylinux.org", Logo: "", Variant: "appliance"},

	// Desktop images — installer ISOs for GUI-focused virtual machines.
	{Name: "fedora-workstation", Description: "Fedora 42 Workstation (GNOME) — live installer ISO",
		ISO: "https://download.fedoraproject.org/pub/fedora/linux/releases/42/Workstation/x86_64/iso/Fedora-Workstation-Live-x86_64-42-1.6.iso", DefaultUser: "fedora", Source: "fedoraproject.org", Logo: "fedora", Variant: "desktop"},
	{Name: "ubuntu-desktop", Description: "Ubuntu 24.04 LTS Desktop — live installer ISO",
		ISO: "https://releases.ubuntu.com/24.04.2/ubuntu-24.04.2-desktop-amd64.iso", DefaultUser: "ubuntu", Source: "ubuntu.com", Logo: "ubuntu", Variant: "desktop"},
	{Name: "windows-11", Description: "Windows 11 — installer ISO (UEFI/TPM, virtio-win drivers)",
		ISO: "https://software.download.prss.microsoft.com/dbazure/Win11_24H2_English_x64.iso", DefaultUser: "administrator", Source: "microsoft.com", Logo: "", Variant: "desktop"},
}

// Find returns the catalog image with the given name, or nil.
func Find(name string) *Image {
	for i := range Images {
		if Images[i].Name == name {
			return &Images[i]
		}
	}
	return nil
}
