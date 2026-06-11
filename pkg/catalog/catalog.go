// Package catalog is Corral's built-in list of ready-to-boot OS images. These
// are KubeVirt containerdisks (cloud images packaged as OCI images, cloud-init
// enabled) from the well-maintained quay.io/containerdisks org — reliable and
// boot directly, no import step. Arbitrary qcow2/raw images can still be brought
// in with `corral create --import <url>` (CDI).
package catalog

// Image is a catalog entry.
type Image struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContainerDisk string `json:"containerDisk"`
	DefaultUser   string `json:"defaultUser"`
}

// Images is the curated catalog.
var Images = []Image{
	{"fedora", "Fedora 42 cloud", "quay.io/containerdisks/fedora:42", "fedora"},
	{"ubuntu", "Ubuntu 24.04 LTS cloud", "quay.io/containerdisks/ubuntu:24.04", "ubuntu"},
	{"ubuntu-22.04", "Ubuntu 22.04 LTS cloud", "quay.io/containerdisks/ubuntu:22.04", "ubuntu"},
	{"debian", "Debian 12 (Bookworm) cloud", "quay.io/containerdisks/debian:12", "debian"},
	{"centos-stream9", "CentOS Stream 9", "quay.io/containerdisks/centos-stream:9", "cloud-user"},
	{"rocky", "Rocky Linux 9", "quay.io/containerdisks/rockylinux:9", "cloud-user"},
	{"almalinux", "AlmaLinux 9", "quay.io/containerdisks/almalinux:9", "cloud-user"},
	{"opensuse-leap", "openSUSE Leap 15.6", "quay.io/containerdisks/opensuse-leap:15.6", "opensuse"},
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
