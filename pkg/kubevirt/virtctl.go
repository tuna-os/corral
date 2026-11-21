package kubevirt

import "fmt"

// VirtctlVersion is the virtctl release that Corral's in-cluster pods
// download: the VNC proxy's init container and the backup CronJob. It is the
// version the corral web image ships (VIRTCTL_VERSION in
// .github/workflows/ci.yml). Change both together, with virtctlSHA256;
// TestVirtctlPinMatchesWorkflow fails when they differ.
const VirtctlVersion = "v1.8.2"

// virtctlSHA256 is the SHA-256 of virtctl-<VirtctlVersion>-linux-<arch>, by
// Go architecture name. These pods run virtctl with cluster credentials
// (consoles, disk exports), and a release file is a mutable name just as an
// image tag is: whoever can replace the file behind the URL would run code
// with that access. The images are pinned by digest for the same reason
// (AlpineImage, #66). A checksum file fetched from the same release would
// not help, because it can be replaced with the binary.
//
// The values are the digests of the virtctl that the corral web image
// carries, which CI downloads from the same URL.
var virtctlSHA256 = map[string]string{
	"amd64": "745f3c58c6a77be65b828e67b6ad930e9039ab293ed5b2bef6d44c8681af5b75",
	"arm64": "345b29874d3c98801ad022ef3074b21e2b21a386b22a2779997ab7c74c7e6f57",
}

// VirtctlInstallScript returns POSIX shell that downloads virtctl
// VirtctlVersion for the node's architecture to dest and makes it executable
// only when its SHA-256 is the expected one. On any failure it removes dest
// and exits non-zero, with or without `sh -e`. `curl -f` makes an HTTP error
// fail: without it, curl would save the error page as virtctl. The digest is
// read with `sha256sum | cut`, which busybox and coreutils both have; when
// sha256sum is missing the comparison fails, so the script fails closed.
func VirtctlInstallScript(dest string) string {
	return virtctlInstallScript(dest, VirtctlVersion, virtctlSHA256)
}

func virtctlInstallScript(dest, version string, sha256 map[string]string) string {
	return fmt.Sprintf(`case "$(uname -m)" in
  x86_64) VIRTCTL_ARCH=amd64 VIRTCTL_SHA256=%[3]s ;;
  aarch64|arm64) VIRTCTL_ARCH=arm64 VIRTCTL_SHA256=%[4]s ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
if ! curl -fsSL -o %[1]s "https://github.com/kubevirt/kubevirt/releases/download/%[2]s/virtctl-%[2]s-linux-${VIRTCTL_ARCH}"; then
  rm -f %[1]s
  exit 1
fi
if [ "$(sha256sum < %[1]s | cut -d' ' -f1)" != "$VIRTCTL_SHA256" ]; then
  rm -f %[1]s
  echo "virtctl %[2]s for ${VIRTCTL_ARCH} does not have the SHA-256 that Corral expects" >&2
  exit 1
fi
chmod +x %[1]s`, dest, version, sha256["amd64"], sha256["arm64"])
}
