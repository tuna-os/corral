package export

// PVE export: vzdump on a fake cluster, download of a recorded archive, and
// vma extraction through stub tools on PATH.
//
// shell.Fake cannot serve here: the "vma extract" step must plant files, and a
// canned runner plants nothing. So these tests use the real runner with stub
// `vma`, `zstd`, and `qemu-img` scripts that record their argv and copy
// fixture bytes — the same contract assertions, against tools that act like
// tools.

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/proxmoxbe"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
)

type pveFixture struct {
	mu          sync.Mutex
	requests    []string
	vzdumpForm  map[string]string
	status      string // "stopped" or "running"
	volID       string // backup volume id the content list reports
	archive     []byte // raw bytes served by the download endpoint
	sawDownload bool
}

func newPVEFixture(status, volID string, archive []byte) *pveFixture {
	return &pveFixture{status: status, volID: volID, archive: archive}
}

func (f *pveFixture) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
}

func (f *pveFixture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		f.record(&http.Request{Method: r.Method, URL: r.URL})

		// The download endpoint streams the archive body, not a JSON envelope.
		if r.Method == "GET" && strings.HasSuffix(path, "/download") {
			f.mu.Lock()
			f.sawDownload = true
			archive := f.archive
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(archive)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		var data any
		switch {
		case r.Method == "GET" && path == "/version":
			data = map[string]any{"version": "8.3.0"}
		case r.Method == "GET" && path == "/cluster/resources":
			data = []map[string]any{
				{"vmid": 100, "name": "web-prod", "node": "pve1", "type": "qemu",
					"status": "stopped", "maxcpu": 4, "maxmem": 8589934592},
				{"vmid": 200, "name": "files-ct", "node": "pve1", "type": "lxc",
					"status": "running", "maxcpu": 1, "maxmem": 536870912},
			}
		case r.Method == "GET" && path == "/storage":
			data = []map[string]any{
				{"storage": "backup-nfs", "type": "nfs", "content": "backup,iso", "shared": 1},
			}
		case r.Method == "GET" && strings.HasSuffix(path, "/status/current"):
			f.mu.Lock()
			status := f.status
			f.mu.Unlock()
			data = map[string]any{"status": status}
		case r.Method == "POST" && strings.HasSuffix(path, "/vzdump"):
			f.mu.Lock()
			f.vzdumpForm = map[string]string{
				"vmid": r.PostForm.Get("vmid"), "storage": r.PostForm.Get("storage"),
				"mode": r.PostForm.Get("mode"),
			}
			f.mu.Unlock()
			data = "UPID:pve1:00001234:0000ABCD:65000000:vzdump:100:corral@pve!ci:"
		case strings.Contains(path, "/tasks/") && strings.HasSuffix(path, "/status"):
			data = map[string]any{"status": "stopped", "exitstatus": "OK", "type": "vzdump"}
		case r.Method == "GET" && strings.HasSuffix(path, "/content"):
			f.mu.Lock()
			volID := f.volID
			f.mu.Unlock()
			data = []map[string]any{{"volid": volID, "content": "backup", "size": 1 << 20}}
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": "no fixture for " + r.Method + " " + path})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
}

// pveSeam points a context at the fake cluster and installs stub tools on
// PATH. The stubs record argv to argvLog and behave like the tools in the one
// way each test needs: vma plants fixture images, zstd copies, qemu-img
// answers info and copies on convert.
func pveSeam(t *testing.T, f *pveFixture, fixtures map[string]string) (string, string) {
	t.Helper()
	server := httptest.NewServer(f.handler())
	t.Cleanup(server.Close)

	client, err := proxmoxbe.New(proxmoxbe.Config{
		Host:  server.URL,
		Token: "corral@pve!ci=00000000-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatalf("proxmoxbe.New: %v", err)
	}
	const context = "test-pve-export"
	proxmoxbe.SetClientForContext(context, client)
	t.Cleanup(proxmoxbe.ResetClients)

	bin := t.TempDir()
	fixtureDir := t.TempDir()
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	writeStub(t, bin, "vma", `echo "vma $*" >> "$ARGV_LOG"
mkdir -p "$3"
if [ "$FAKE_VMA_EMPTY" = "1" ]; then exit 0; fi
cp "$FAKE_VMA_FIXTURES"/*.img "$3"/
`, map[string]string{"ARGV_LOG": argvLog, "FAKE_VMA_FIXTURES": fixtureDir})
	writeStub(t, bin, "zstd", `echo "zstd $*" >> "$ARGV_LOG"
cp "$2" "$4"
`, map[string]string{"ARGV_LOG": argvLog})
	writeStub(t, bin, "qemu-img", `echo "qemu-img $*" >> "$ARGV_LOG"
if [ "$1" = "info" ]; then echo '{"virtual-size":4096}'; exit 0; fi
prev=""; last=""
for a in "$@"; do prev="$last"; last="$a"; done
cp "$prev" "$last"
`, map[string]string{"ARGV_LOG": argvLog})

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	SetRunner(shell.Real{})
	t.Cleanup(func() { SetRunner(shell.Real{}) })
	return context, argvLog
}

func writeStub(t *testing.T, bin, name, body string, env map[string]string) {
	t.Helper()
	// Env baked in: fixture dirs must not leak between tests via the process env.
	lines := []string{"#!/bin/sh"}
	for k, v := range env {
		lines = append(lines, k+"="+v, "export "+k)
	}
	script := strings.Join(lines, "\n") + "\n" + body
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func argvLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func pveRef(context string) types.InstanceRef {
	return types.InstanceRef{Backend: "proxmox", Context: context, Name: "web-prod"}
}

// The whole pipeline against recorded payloads: resolve, snapshot vzdump,
// task wait, latest-backup selection, download, extract, convert.
func TestPVEExport_FullVzdumpRoundTrip(t *testing.T) {
	f := newPVEFixture("stopped",
		"backup-nfs:backup/vzdump-qemu-100-2026_10_12-00_00_00.vma.zst",
		[]byte("VMA-BYTES"))
	ctx, argv := pveSeam(t, f, map[string]string{"drive-scsi0.img": "BOOTDISK"})

	dest := filepath.Join(t.TempDir(), "web-prod.qcow2")
	result, err := (PVE{}).Export(context.Background(), Request{Ref: pveRef(ctx), Dest: dest}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.Format != Qcow2 {
		t.Errorf("format = %q, want qcow2", result.Format)
	}
	if result.Consistency != snapshot.Offline {
		t.Errorf("consistency = %q, want offline for a stopped guest", result.Consistency)
	}
	if got, _ := os.ReadFile(dest); string(got) != "BOOTDISK" {
		t.Errorf("dest holds %q, want the extracted boot disk", got)
	}

	// vzdump must run in snapshot mode against the resolved guest on the
	// backup storage — anything else costs downtime or writes nowhere.
	f.mu.Lock()
	form := f.vzdumpForm
	sawDownload := f.sawDownload
	f.mu.Unlock()
	if form["vmid"] != "100" || form["storage"] != "backup-nfs" {
		t.Errorf("vzdump targeted %v, want vmid 100 on backup-nfs", form)
	}
	if form["mode"] != "snapshot" {
		t.Errorf("vzdump mode = %q, want snapshot (no downtime)", form["mode"])
	}
	if !sawDownload {
		t.Error("the backup archive was never downloaded")
	}

	// The scratch files are gigabytes in production; all must be gone.
	for _, leftover := range []string{dest + ".vma.zst", dest + ".vma", dest + ".img"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", leftover)
		}
	}
	if log := argvLog(t, argv); !strings.Contains(log, "vma extract") {
		t.Errorf("vma extract was never invoked:\n%s", log)
	}
}

// A running guest exports crash-consistent and says so.
func TestPVEExport_RunningGuestIsCrashConsistent(t *testing.T) {
	f := newPVEFixture("running",
		"backup-nfs:backup/vzdump-qemu-100-2026_10_12-00_00_00.vma.zst",
		[]byte("VMA-BYTES"))
	ctx, _ := pveSeam(t, f, map[string]string{"drive-scsi0.img": "BOOTDISK"})

	dest := filepath.Join(t.TempDir(), "web-prod.qcow2")
	result, err := (PVE{}).Export(context.Background(), Request{Ref: pveRef(ctx), Dest: dest}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.Consistency != snapshot.Crash {
		t.Errorf("consistency = %q, want crash for a running guest", result.Consistency)
	}
}

// Several disks in one backup: the boot disk (lowest unit) must win, never an
// arbitrary readdir pick. os.ReadDir sorts, so scsi0 is first — the assertion
// pins that the adapter relies on nothing weaker.
func TestPVEExport_MultiDiskSelectsTheBootDisk(t *testing.T) {
	f := newPVEFixture("stopped",
		"backup-nfs:backup/vzdump-qemu-100-2026_10_12-00_00_00.vma.zst",
		[]byte("VMA-BYTES"))
	_, _ = pveSeam(t, f, map[string]string{
		"drive-scsi0.img":   "BOOTDISK",
		"drive-scsi1.img":   "DATADISK",
		"drive-virtio1.img": "DATADISK2",
	})

	archive := filepath.Join(t.TempDir(), "backup.vma")
	if err := os.WriteFile(archive, []byte("VMA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := archive + ".disk"
	if err := extractVMADisk(archive, dest); err != nil {
		t.Fatalf("extractVMADisk: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "BOOTDISK" {
		t.Errorf("extracted %q, want the boot disk (scsi0), not a data disk", got)
	}
}

// Compression routing: a .zst archive goes through zstd -d first; a plain
// .vma goes straight to vma. (.gz is not decompressed — vma receives it as-is,
// which the assertion documents.)
func TestPVEExtract_ZstdVsPlainArchive(t *testing.T) {
	f := newPVEFixture("stopped", "vol", []byte("x"))
	_, argv := pveSeam(t, f, map[string]string{"drive-scsi0.img": "BOOTDISK"})

	dir := t.TempDir()

	zst := filepath.Join(dir, "backup.vma.zst")
	if err := os.WriteFile(zst, []byte("ZST-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractVMADisk(zst, filepath.Join(dir, "z.img")); err != nil {
		t.Fatalf("zst extract: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "z.img")); string(got) != "BOOTDISK" {
		t.Errorf("zst extract holds %q", got)
	}

	plain := filepath.Join(dir, "backup.vma")
	if err := os.WriteFile(plain, []byte("VMA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractVMADisk(plain, filepath.Join(dir, "p.img")); err != nil {
		t.Fatalf("plain extract: %v", err)
	}

	log := argvLog(t, argv)
	if !strings.Contains(log, "zstd -d "+zst+" ") {
		t.Errorf("the zstd archive was not decompressed first:\n%s", log)
	}
	// Exactly one zstd call (for the .zst archive) and one vma call per
	// archive: the plain .vma must reach vma without a decompression step.
	var zstdCalls, vmaCalls int
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		switch {
		case strings.HasPrefix(line, "zstd "):
			zstdCalls++
			if !strings.Contains(line, zst) {
				t.Errorf("unexpected decompression target: %q", line)
			}
		case strings.HasPrefix(line, "vma "):
			vmaCalls++
		}
	}
	if zstdCalls != 1 {
		t.Errorf("zstd ran %d times, want once (the .zst archive only):\n%s", zstdCalls, log)
	}
	if vmaCalls != 2 {
		t.Errorf("vma ran %d times, want once per archive:\n%s", vmaCalls, log)
	}
}

// No vma tool: an explicit refusal naming the package, not a command-not-found.
func TestPVEExtract_VmaAbsenceRefusedWithRemedy(t *testing.T) {
	old := vmaBinaryFn
	vmaBinaryFn = func() string { return "" }
	t.Cleanup(func() { vmaBinaryFn = old })

	archive := filepath.Join(t.TempDir(), "backup.vma")
	if err := os.WriteFile(archive, []byte("VMA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractVMADisk(archive, filepath.Join(t.TempDir(), "disk.img"))
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("want a typed refusal, got %T: %v", err, err)
	}
	if !strings.Contains(u.Reason, "pve-qemu-kvm") {
		t.Errorf("refusal does not name the package: %q", u.Reason)
	}
	if u.Remedy == "" {
		t.Error("refusal offers no way forward")
	}
}

// A backup that yields no drive image is an error naming the expectation, not
// an empty disk handed downstream.
func TestPVEExtract_NoDiskImageIsAnError(t *testing.T) {
	f := newPVEFixture("stopped", "vol", []byte("x"))
	_, _ = pveSeam(t, f, map[string]string{"drive-scsi0.img": "BOOTDISK"})
	t.Setenv("FAKE_VMA_EMPTY", "1")

	archive := filepath.Join(t.TempDir(), "backup.vma")
	if err := os.WriteFile(archive, []byte("VMA-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractVMADisk(archive, filepath.Join(t.TempDir(), "disk.img"))
	if err == nil {
		t.Fatal("an archive with no disk image must fail, not produce an empty disk")
	}
	if !strings.Contains(err.Error(), "drive-") {
		t.Errorf("error should name the expected member pattern, got: %v", err)
	}
}

// A container has no disk image — refused up front, before any vzdump.
func TestPVEExport_RefusesAContainer(t *testing.T) {
	f := newPVEFixture("stopped", "vol", []byte("x"))
	ctx, _ := pveSeam(t, f, map[string]string{"drive-scsi0.img": "BOOTDISK"})

	ref := types.InstanceRef{Backend: "proxmox", Context: ctx, Name: "files-ct"}
	_, err := (PVE{}).Export(context.Background(),
		Request{Ref: ref, Dest: filepath.Join(t.TempDir(), "ct.qcow2")}, nil)
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("want a typed refusal, got %T: %v", err, err)
	}
	if !strings.Contains(u.Reason, "container") {
		t.Errorf("refusal does not name the cause: %q", u.Reason)
	}
}

// RawGz through the full pipeline: qemu-img to raw, then gzip — and the
// artifact on disk must be a gzip stream that decompresses to the boot disk.
func TestPVEExport_RawGzPipeline(t *testing.T) {
	f := newPVEFixture("stopped",
		"backup-nfs:backup/vzdump-qemu-100-2026_10_12-00_00_00.vma.zst",
		[]byte("VMA-BYTES"))
	ctx, _ := pveSeam(t, f, map[string]string{"drive-scsi0.img": "BOOTDISK"})

	dest := filepath.Join(t.TempDir(), "web-prod.img.gz")
	result, err := (PVE{}).Export(context.Background(),
		Request{Ref: pveRef(ctx), Dest: dest, Format: RawGz}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.Format != RawGz {
		t.Errorf("format = %q, want raw.gz", result.Format)
	}
	fh, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatalf("artifact is not gzip: %v", err)
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "BOOTDISK" {
		t.Errorf("decompressed artifact holds %q, want the boot disk", raw)
	}
}
