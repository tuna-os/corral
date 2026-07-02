package kubevirt

import (
	"io"
	"os/exec"
)

// UploadDataVolume uploads a local disk image (already staged on this
// machine's filesystem — see pkg/web's handler, which streams the browser's
// upload to a temp file first) into a new DataVolume via the CDI upload
// proxy. Reuses `virtctl image-upload` rather than re-implementing CDI's
// upload protocol (UploadTokenRequest + streaming to cdi-uploadproxy with a
// bearer token) — virtctl already does that robustly, including retries and
// progress reporting on its combined output.
//
// storageClass "" lets CDI/the cluster pick its default. --insecure is
// passed because CDI's uploadproxy typically serves a self-signed cert
// unless the cluster operator has wired in a trusted one — same trust model
// as this package's other unauthenticated-by-default cluster-internal
// traffic (the tailnet + K8s RBAC are the actual auth boundary, not TLS
// chain validation here).
func UploadDataVolume(name, namespace, imagePath, size, storageClass string, progress io.Writer) error {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	virtctl, err := NewClient(namespace).Virtctl()
	if err != nil {
		return err
	}

	args := []string{"image-upload", "dv", name, "-n", namespace,
		"--image-path", imagePath, "--size", size, "--insecure"}
	if storageClass != "" {
		args = append(args, "--storage-class", storageClass)
	}
	cmd := exec.Command(virtctl, args...)
	cmd.Stdout = progress
	cmd.Stderr = progress
	return cmd.Run()
}
