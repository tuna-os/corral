package web

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// handleExport streams a VM disk backup (gzip) to the browser. The VM must be
// stopped (its RWO disk can't be read while running). The disk is exported to a
// pod-local temp file via virtctl, then streamed and removed.
func handleExport(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if _, err := defaultRunner.Run("kubectl", "get", "vmi", name, "-n", ns); err == nil {
		errResp(w, http.StatusConflict, fmt.Errorf("stop %s before exporting (its disk is in use while running)", name))
		return
	}
	if r.URL.Query().Get("format") == "qcow2" {
		exportQcow2(w, r, ns, name)
		return
	}
	tmp, err := os.CreateTemp("", name+"-*.img.gz")
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := kubeClient(r, ns).Export(name, "", tmpName); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	f, err := os.Open(tmpName)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".img.gz"))
	if st, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	_, _ = io.Copy(w, f)
}

// exportQcow2 downloads the VM disk raw, converts it to compressed qcow2 with
// qemu-img, and streams the result. Needs qemu-img on the server and scratch
// space sized to the raw disk; degrades to a clear error otherwise (the default
// raw.gz export still works without either).
func exportQcow2(w http.ResponseWriter, r *http.Request, ns, name string) {
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		errResp(w, http.StatusNotImplemented,
			fmt.Errorf("qcow2 export needs qemu-img on the server (not installed); use the default raw.gz download instead"))
		return
	}
	dir, err := os.MkdirTemp("", "corral-export-"+name+"-*")
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	rawPath := filepath.Join(dir, name+".raw")
	qcowPath := filepath.Join(dir, name+".qcow2")

	if _, err := kubeClient(r, ns).ExportRaw(name, "", rawPath); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	if err := convertRawToQcow2(qemuImg, rawPath, qcowPath); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	_ = os.Remove(rawPath) // free the raw before streaming the qcow2

	f, err := os.Open(qcowPath)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".qcow2"))
	if st, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	_, _ = io.Copy(w, f)
}

// convertRawToQcow2 runs qemu-img to turn a raw disk into a zlib-compressed
// qcow2 (-c): compact, seekable, and re-importable by CDI/qemu. Extracted so the
// conversion (and its exact flags) is unit-testable with a real qemu-img.
func convertRawToQcow2(qemuImg, rawPath, qcowPath string) error {
	cmd := exec.Command(qemuImg, "convert", "-f", "raw", "-O", "qcow2", "-c", rawPath, qcowPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("qemu-img convert: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
