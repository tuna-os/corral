package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hanthor/corral/pkg/catalog"
	"github.com/hanthor/corral/pkg/doctor"
	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/plugin"
	"github.com/hanthor/corral/pkg/types"
)

// ── Doctor (cluster diagnostics) ──────────────────────────────────

// GET /api/doctor — run the diagnostics.
func handleDoctor(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, doctor.Run())
}

// POST /api/doctor/fix — reconcile the fixable issues.
// Body {"check": "<name>"} fixes just that check; empty body fixes all.
func handleDoctorFix(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Check string `json:"check"`
	}
	json.NewDecoder(r.Body).Decode(&b) // empty body = fix all
	if b.Check != "" {
		done := taskBegin("doctor fix", b.Check)
		if err := doctor.FixOne(b.Check); err != nil {
			done(err)
			errResp(w, http.StatusInternalServerError, err)
			return
		}
		done(nil)
		jsonResp(w, http.StatusOK, map[string]any{"fixed": []string{b.Check}})
		return
	}
	done := taskBegin("doctor fix", "all fixable")
	fixed, err := doctor.Fix()
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"fixed": fixed})
}

// ── Extensions (plugins) store ────────────────────────────────────

type pluginItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Homepage    string `json:"homepage,omitempty"`
	Installed   bool   `json:"installed"`
	InStore     bool   `json:"inStore"`
}

// GET /api/plugins — marketplace entries merged with installed state.
func handlePlugins(w http.ResponseWriter, r *http.Request) {
	installed := map[string]bool{}
	for _, p := range plugin.Installed() {
		installed[p.Name] = true
	}
	items := []pluginItem{}
	seen := map[string]bool{}
	if idx, err := plugin.FetchIndex(); err == nil {
		for _, e := range idx.Plugins {
			items = append(items, pluginItem{e.Name, e.Description, e.Version, e.Homepage, installed[e.Name], true})
			seen[e.Name] = true
		}
	}
	for _, p := range plugin.Installed() {
		if !seen[p.Name] {
			items = append(items, pluginItem{Name: p.Name, Installed: true})
		}
	}
	jsonResp(w, http.StatusOK, items)
}

// POST /api/plugins/{name}/install
func handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	idx, err := plugin.FetchIndex()
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	e := idx.Find(name)
	if e == nil {
		errResp(w, http.StatusNotFound, fmt.Errorf("no plugin %q in the marketplace", name))
		return
	}
	done := taskBegin("install plugin", name)
	if err := e.Install(); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"installed": name})
}

// DELETE /api/plugins/{name}
func handleRemovePlugin(w http.ResponseWriter, r *http.Request) {
	if err := plugin.Remove(r.PathValue("name")); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "removed"})
}

// This file holds the HTTP handlers for the advanced VM operations
// (scale, volumes, expand, snapshots, clone, guest info, capabilities).
// Registered in server.go alongside the basic lifecycle routes.

// handleCapabilities reports cluster storage capabilities so the UI can
// enable/disable expand and snapshot controls.
func handleCapabilities(w http.ResponseWriter, r *http.Request) {
	c := kubevirt.ClusterCapabilities()
	// Installed plugins, so the UI can light up their features
	// (the bootc flag stays separate: it's compile-time, not a binary on disk).
	plugins := []string{}
	for _, p := range plugin.Installed() {
		plugins = append(plugins, p.Name)
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"storageClass":     c.StorageClass,
		"canExpand":        c.CanExpand,
		"canSnapshot":      c.CanSnapshot,
		"bootc":            kubevirt.BootcAvailable(), // optional plugin
		"plugins":          plugins,
		"defaultNamespace": kubevirt.DefaultNamespace,
	})
}

// POST /api/vms/{ns}/{name}/scale  body: {cpu, mem}
func handleScale(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		CPU int    `json:"cpu"`
		Mem string `json:"mem"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	done := taskBegin("scale", ns+"/"+name)
	if err := kubevirt.NewClient(ns).Scale(name, b.CPU, b.Mem); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/vms/{ns}/{name}/volumes  body: {size}
func handleAddVolume(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		Size string `json:"size"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	done := taskBegin("add disk", ns+"/"+name)
	pvc, err := kubevirt.NewClient(ns).AddVolume(name, b.Size)
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"pvc": pvc})
}

// DELETE /api/vms/{ns}/{name}/volumes/{vol}
func handleRemoveVolume(w http.ResponseWriter, r *http.Request) {
	ns, name, vol := r.PathValue("ns"), r.PathValue("name"), r.PathValue("vol")
	done := taskBegin("detach disk", ns+"/"+name)
	if err := kubevirt.NewClient(ns).RemoveVolume(name, vol); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "removed"})
}

// POST /api/vms/{ns}/{name}/expand  body: {pvc, size}
func handleExpand(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	var b struct {
		PVC  string `json:"pvc"`
		Size string `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.PVC == "" || b.Size == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("pvc and size are required"))
		return
	}
	done := taskBegin("expand disk", ns+"/"+b.PVC)
	if err := kubevirt.NewClient(ns).ExpandDisk(b.PVC, b.Size); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/vms/{ns}/{name}/snapshots
func handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	snaps, err := kubevirt.NewClient(ns).ListSnapshots(name)
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	jsonResp(w, http.StatusOK, snaps)
}

// POST /api/vms/{ns}/{name}/snapshots  body: {name?}
func handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	done := taskBegin("snapshot", ns+"/"+name)
	snap, err := kubevirt.NewClient(ns).Snapshot(name, b.Name)
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"name": snap})
}

// POST /api/vms/{ns}/{name}/snapshots/{snap}/restore
func handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	ns, name, snap := r.PathValue("ns"), r.PathValue("name"), r.PathValue("snap")
	done := taskBegin("restore snapshot", ns+"/"+name)
	if err := kubevirt.NewClient(ns).RestoreSnapshot(name, snap); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "restoring"})
}

// DELETE /api/vms/{ns}/{name}/snapshots/{snap}
func handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	ns, snap := r.PathValue("ns"), r.PathValue("snap")
	done := taskBegin("delete snapshot", ns+"/"+snap)
	if err := kubevirt.NewClient(ns).DeleteSnapshot(snap); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// POST /api/vms/{ns}/{name}/clone  body: {target}
func handleClone(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Target == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("target name is required"))
		return
	}
	done := taskBegin("clone", ns+"/"+name+" → "+b.Target)
	if err := kubevirt.NewClient(ns).Clone(name, b.Target); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	if store != nil {
		store.Set(b.Target, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
	}
	jsonResp(w, http.StatusOK, map[string]string{"target": b.Target})
}

// GET /api/vms/{ns}/{name}/guestinfo
func handleGuestInfo(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	info, err := kubevirt.NewClient(ns).GuestInfo(name)
	if err != nil {
		errResp(w, http.StatusServiceUnavailable, err)
		return
	}
	jsonResp(w, http.StatusOK, info)
}

// GET /api/vms/{ns}/{name}/events
func handleEvents(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	evs, err := kubevirt.NewClient(ns).Events(name)
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	jsonResp(w, http.StatusOK, evs)
}

// GET /api/vms/{ns}/{name}/metrics
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	jsonResp(w, http.StatusOK, kubevirt.NewClient(ns).Metrics(name))
}

// POST /api/vms/{ns}/{name}/template  body: {on: bool}  — mark/unmark template
func handleMarkTemplate(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		On bool `json:"on"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if err := kubevirt.NewClient(ns).MarkTemplate(name, b.On); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"isTemplate": b.On})
}

// GET /api/nads — Multus NetworkAttachmentDefinitions for secondary NICs
func handleNADs(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, kubevirt.ListNADs())
}

// POST /api/vms/{ns}/{name}/nics  body: {nad, iface}
func handleAddNIC(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		NAD   string `json:"nad"`
		Iface string `json:"iface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.NAD == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("nad is required"))
		return
	}
	if err := kubevirt.NewClient(ns).AddNIC(name, b.NAD, b.Iface); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/vms/{ns}/{name}/bootc/rebuild  body: {image?}
// Rebuilds a bootc VM's disk (from its recorded image, or the given override
// to switch images) and restarts it. Runs as a background task with a live
// build log, like the initial bootc create.
func handleBootcRebuild(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !kubevirt.BootcAvailable() {
		errResp(w, http.StatusBadRequest,
			fmt.Errorf("bootc support is not enabled on this server (optional plugin)"))
		return
	}
	var b struct {
		Image string `json:"image"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	image := catalog.ResolveBootc(b.Image)
	if image == "" && store != nil {
		if e, ok := store.Get(name); ok {
			image = e.Extra["bootc_image"]
		}
	}
	if image == "" {
		// The in-pod registry is lost on restart; fall back to the durable image
		// annotation recorded on the VM at create time.
		image = kubevirt.BootcImageOf(name, ns)
	}
	if image == "" {
		errResp(w, http.StatusBadRequest,
			fmt.Errorf("no recorded bootc image for %q — pass an image to switch to", name))
		return
	}
	sshKey := kubevirt.LoadSSHPublicKey()

	id := fmt.Sprintf("bootc-rebuild-%s-%d", name, time.Now().UnixNano())
	task := &buildTask{status: "running"}
	tasks.Store(id, task)
	done := taskBegin("bootc rebuild", ns+"/"+name)
	go func() {
		err := kubevirt.BootcRebuild(name, ns, image, sshKey, "", task)
		if err == nil && store != nil {
			store.Set(name, types.RegistryEntry{
				Backend: "kubevirt", Namespace: ns,
				Extra: map[string]string{"bootc_image": image},
			})
		}
		task.finish(err)
		done(err)
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{"task": id})
}

// GET /api/images — the built-in OS image catalog.
// ?type=bootc returns the curated bootc image catalog instead.
func handleImages(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("type") == "bootc" {
		jsonResp(w, http.StatusOK, catalog.BootcImages)
		return
	}
	jsonResp(w, http.StatusOK, catalog.Images)
}

// GET /api/instancetypes — cluster instancetypes + preferences for the create wizard
func handleInstanceTypes(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, map[string][]string{
		"instancetypes": kubevirt.ListInstanceTypes(),
		"preferences":   kubevirt.ListPreferences(),
	})
}

// GET /api/datavolumes — image/ISO library
func handleListDataVolumes(w http.ResponseWriter, r *http.Request) {
	dvs, err := kubevirt.ListDataVolumes()
	if err != nil {
		errResp(w, http.StatusBadGateway, err)
		return
	}
	jsonResp(w, http.StatusOK, dvs)
}

// POST /api/datavolumes  body: {name, namespace, url, size}
func handleImportDataVolume(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name, Namespace, URL, Size string
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Name == "" || b.URL == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("name and url are required"))
		return
	}
	done := taskBegin("import image", b.Namespace+"/"+b.Name)
	if err := kubevirt.ImportDataVolume(b.Name, b.Namespace, b.URL, b.Size); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"name": b.Name})
}

// DELETE /api/datavolumes/{ns}/{name}
func handleDeleteDataVolume(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	done := taskBegin("delete image", ns+"/"+name)
	if err := kubevirt.DeleteDataVolume(ns, name); err != nil {
		done(err)
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}
