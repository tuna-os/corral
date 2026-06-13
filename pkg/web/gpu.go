package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// GPU passthrough — the GUI half of the gpu plugin. Discovering and permitting
// devices in the KubeVirt CR is an admin op kept in the CLI (`corral gpu
// enable`); the web UI does the per-VM part: list permitted devices and
// attach/detach them. Patches spec.template.spec.domain.devices.gpus directly
// (compiled in — no plugin binary needed on the server).

type gpuDevice struct {
	ResourceName string `json:"resourceName"`
	Selector     string `json:"selector"`
	Type         string `json:"type"` // "pci" or "mediated"
}

// GET /api/gpus — passthrough devices permitted in the KubeVirt CR.
func handleListGPUs(w http.ResponseWriter, r *http.Request) {
	out, err := defaultRunner.Run("kubectl", "get", "kubevirt", "kubevirt",
		"-n", "kubevirt", "-o", "json")
	if err != nil {
		jsonResp(w, http.StatusOK, []gpuDevice{}) // no KubeVirt CR / none permitted
		return
	}
	var kv struct {
		Spec struct {
			Configuration struct {
				PermittedHostDevices struct {
					PCIHostDevices []struct {
						PCIVendorSelector string `json:"pciVendorSelector"`
						ResourceName      string `json:"resourceName"`
					} `json:"pciHostDevices"`
					MediatedDevices []struct {
						MdevNameSelector string `json:"mdevNameSelector"`
						ResourceName     string `json:"resourceName"`
					} `json:"mediatedDevices"`
				} `json:"permittedHostDevices"`
			} `json:"configuration"`
		} `json:"spec"`
	}
	if json.Unmarshal(out, &kv) != nil {
		jsonResp(w, http.StatusOK, []gpuDevice{})
		return
	}
	devs := []gpuDevice{}
	for _, d := range kv.Spec.Configuration.PermittedHostDevices.PCIHostDevices {
		devs = append(devs, gpuDevice{ResourceName: d.ResourceName, Selector: d.PCIVendorSelector, Type: "pci"})
	}
	for _, d := range kv.Spec.Configuration.PermittedHostDevices.MediatedDevices {
		devs = append(devs, gpuDevice{ResourceName: d.ResourceName, Selector: d.MdevNameSelector, Type: "mediated"})
	}
	jsonResp(w, http.StatusOK, devs)
}

type vmGPU struct {
	Name       string `json:"name"`
	DeviceName string `json:"deviceName"`
}

func vmGPUList(ns, name string) ([]vmGPU, error) {
	out, err := defaultRunner.Run("kubectl", "get", "vm", name, "-n", ns, "-o",
		"jsonpath={.spec.template.spec.domain.devices.gpus}")
	if err != nil {
		return nil, fmt.Errorf("reading VM %s: %s", name, strings.TrimSpace(string(out)))
	}
	var gpus []vmGPU
	if s := strings.TrimSpace(string(out)); s != "" {
		if err := json.Unmarshal([]byte(s), &gpus); err != nil {
			return nil, err
		}
	}
	return gpus, nil
}

func patchVMGPUList(ns, name string, gpus []vmGPU) error {
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"domain": map[string]any{"devices": map[string]any{"gpus": gpus}},
	}}}}
	body, _ := json.Marshal(patch)
	out, err := defaultRunner.Run("kubectl", "patch", "vm", name, "-n", ns,
		"--type", "merge", "-p", string(body))
	if err != nil {
		return fmt.Errorf("patching VM: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GET /api/vms/{ns}/{name}/gpus — devices attached to the VM.
func handleGetVMGPUs(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	gpus, err := vmGPUList(ns, name)
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	if gpus == nil {
		gpus = []vmGPU{}
	}
	jsonResp(w, http.StatusOK, gpus)
}

// POST /api/vms/{ns}/{name}/gpus  body: {device, name?} — attach a permitted device.
func handleAttachGPU(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		Device string `json:"device"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Device == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("device (resourceName) is required"))
		return
	}
	gpus, err := vmGPUList(ns, name)
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	if b.Name == "" {
		b.Name = fmt.Sprintf("gpu%d", len(gpus)+1)
	}
	for _, g := range gpus {
		if g.Name == b.Name {
			errResp(w, http.StatusConflict, fmt.Errorf("VM already has a GPU named %q", b.Name))
			return
		}
	}
	done := taskBegin("attach GPU", ns+"/"+name)
	gpus = append(gpus, vmGPU{Name: b.Name, DeviceName: b.Device})
	if err := patchVMGPUList(ns, name, gpus); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"name": b.Name, "device": b.Device})
}

// DELETE /api/vms/{ns}/{name}/gpus/{gpu} — detach by gpu name.
func handleDetachGPU(w http.ResponseWriter, r *http.Request) {
	ns, name, gpu := r.PathValue("ns"), r.PathValue("name"), r.PathValue("gpu")
	gpus, err := vmGPUList(ns, name)
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	kept := []vmGPU{}
	for _, g := range gpus {
		if g.Name != gpu {
			kept = append(kept, g)
		}
	}
	if len(kept) == len(gpus) {
		errResp(w, http.StatusNotFound, fmt.Errorf("no GPU named %q on %s", gpu, name))
		return
	}
	done := taskBegin("detach GPU", ns+"/"+name)
	if err := patchVMGPUList(ns, name, kept); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}
