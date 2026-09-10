package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestGenerateWindowsVM_Shape(t *testing.T) {
	vm := generateWindowsVM("win11", "tailvm", "8Gi", 4, false)
	b, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("manifest does not marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"kind":"VirtualMachine"`,
		`"name":"win11"`,
		`"type":"q35"`,
		`"efi":{"secureBoot":false}`,
		`"tpm":{}`,
		`"hyperv"`,
		`"model":"e1000e"`,
		`"claimName":"win11-disk"`,
		`"claimName":"win11-iso"`,
		VirtioWinImage,
		`"cores":4`,
		`"memory":"8Gi"`,
		`"corral.dev/os":"windows"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("windows VM manifest missing %q", want)
		}
	}
}

func TestGenerateWindowsVM_BootOrder(t *testing.T) {
	vm := generateWindowsVM("win11", "tailvm", "8Gi", 4, false)
	spec := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	disks := spec["domain"].(map[string]any)["devices"].(map[string]any)["disks"].([]map[string]any)

	order := map[string]any{}
	for _, d := range disks {
		order[d["name"].(string)] = d["bootOrder"]
	}
	// Disk first so an installed system boots straight from disk; the empty
	// disk falls through to the installer ISO on first boot.
	if order["rootdisk"] != 1 || order["windows-iso"] != 2 {
		t.Errorf("boot order = %v, want rootdisk=1 windows-iso=2", order)
	}
	if _, hasOrder := disks[2]["bootOrder"]; hasOrder {
		t.Error("the driver CD-ROM must not be bootable")
	}
}

func TestCreateWindowsVM_AppliesAllManifests(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir()) // registry write goes to a scratch HOME

	if err := createWindowsVM("win11", "tailvm", "https://example.com/win11.iso",
		"64Gi", "8Gi", 4, false); err != nil {
		t.Fatalf("createWindowsVM: %v", err)
	}
	applies := 0
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	// No Tailscale operator fake registered, so ApplyProxy skips early:
	// just ISO DataVolume + boot PVC + VM.
	if applies != 3 {
		t.Errorf("applied %d manifests, want 3", applies)
	}
}

func TestCreateWindowsVM_UnattendedAppliesConfigMapAndReturnsPassword(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir())

	if err := createWindowsVM("win11", "tailvm", "https://example.com/win11.iso",
		"64Gi", "8Gi", 4, true); err != nil {
		t.Fatalf("createWindowsVM: %v", err)
	}
	applies, sawConfigMap := 0, false
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
			if strings.Contains(c.Stdin, "autounattend.xml") {
				sawConfigMap = true
			}
		}
	}
	// ISO DV + boot PVC + autounattend ConfigMap + VM.
	if applies != 4 {
		t.Errorf("applied %d manifests, want 4 (unattended adds the ConfigMap)", applies)
	}
	if !sawConfigMap {
		t.Error("expected an autounattend.xml ConfigMap to be applied")
	}
}

func TestCreateWindowsVM_ExposesConsolePorts(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir())

	// ApplyProxy only exposes the VM when the Tailscale operator is present
	// (it probes for the `tailscale` IngressClass) — pretend it is here.
	fake.AddResponseKV("kubectl", []string{"get", "ingressclass", "tailscale"}, "tailscale", nil)

	if err := createWindowsVM("win11", "tailvm", "https://example.com/win11.iso",
		"64Gi", "8Gi", 4, false); err != nil {
		t.Fatalf("createWindowsVM: %v", err)
	}
	// ISO DV + boot PVC + VM + proxy RBAC + proxy Service + proxy Deployment
	applies, sawSSH, sawVNC, sawRDP := 0, false, false, false
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
			if strings.Contains(c.Stdin, "port-22") {
				sawSSH = true
			}
			if strings.Contains(c.Stdin, "5900") {
				sawVNC = true
			}
			if strings.Contains(c.Stdin, "3389") {
				sawRDP = true
			}
		}
	}
	if applies < 6 {
		t.Errorf("applied %d manifests, want >= 6 (VM trio + proxy trio)", applies)
	}
	if !sawSSH || !sawVNC || !sawRDP {
		t.Errorf("expected proxy to expose SSH+VNC+RDP unconditionally, got ssh=%v vnc=%v rdp=%v", sawSSH, sawVNC, sawRDP)
	}
}

func TestAttachDrivers(t *testing.T) {
	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl patch vm win11 -n tailvm --type json -p", "patched", nil)
	runner = fake
	t.Cleanup(func() { runner = shell.Real{} })

	if err := attachDrivers("win11", "tailvm"); err != nil {
		t.Fatalf("attachDrivers: %v", err)
	}
	patch := strings.Join(fake.Calls()[0].Args, " ")
	if !strings.Contains(patch, VirtioWinImage) || !strings.Contains(patch, `"cdrom"`) {
		t.Errorf("patch missing driver CD-ROM: %s", patch)
	}
}

func TestAttachDrivers_VMNotFound(t *testing.T) {
	fake := shell.NewFake() // unregistered → patch fails
	runner = fake
	t.Cleanup(func() { runner = shell.Real{} })

	if err := attachDrivers("ghost", "tailvm"); err == nil {
		t.Fatal("attachDrivers should fail when the VM does not exist")
	}
}

// ── context and ISO plumbing ──────────────────────────────────────
//
// The plugin binaries are shipped through the marketplace and installed on
// operators' machines, and these are the parts of them that run before
// anything else does: which context a command acts on, and where the
// virtio-win ISO comes from. Both were untested.

func TestTarget_DefaultsAndUnknownContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// No context named: the default one, whatever the host is configured for.
	got, err := target("")
	if err != nil {
		t.Fatalf("target(\"\"): %v", err)
	}
	if got.Backend == "" {
		t.Error("the default context must name a backend")
	}

	// A name nothing matches is an error that says where to look, not a
	// silent fall back to the default — creating a Windows VM on the wrong
	// cluster is not a recoverable mistake.
	_, err = target("no-such-context")
	if err == nil {
		t.Fatal("an unknown context must be an error")
	}
	if !strings.Contains(err.Error(), "corral context ls") {
		t.Errorf("error = %v, want it to point at `corral context ls`", err)
	}
}

func TestResolveVirtioISO_PrefersAnExplicitPath(t *testing.T) {
	got, err := resolveVirtioISO("/isos/virtio-win.iso")
	if err != nil {
		t.Fatalf("resolveVirtioISO: %v", err)
	}
	if got != "/isos/virtio-win.iso" {
		t.Errorf("resolveVirtioISO = %q, want the path the operator gave", got)
	}
}

// A download that dies half way must not leave a truncated ISO in the cache:
// it would be reused forever, and Windows Setup's failure would point at
// virtio rather than at the broken file.
func TestDownloadTo(t *testing.T) {
	t.Run("writes the payload", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("iso-bytes"))
		}))
		defer srv.Close()

		dest := filepath.Join(t.TempDir(), "virtio-win.iso")
		if err := downloadTo(srv.URL, dest); err != nil {
			t.Fatalf("downloadTo: %v", err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("reading %s: %v", dest, err)
		}
		if string(got) != "iso-bytes" {
			t.Errorf("downloaded %q, want %q", got, "iso-bytes")
		}
		if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
			t.Error("the .part file should be renamed away, not left behind")
		}
	})

	t.Run("a non-200 leaves nothing behind", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		dest := filepath.Join(t.TempDir(), "virtio-win.iso")
		err := downloadTo(srv.URL, dest)
		if err == nil {
			t.Fatal("a 404 must be an error")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("error = %v, want the status in it", err)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Error("nothing should be written when the download fails")
		}
	})
}
