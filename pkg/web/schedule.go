package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tuna-os/corral/pkg/cronops"
	"github.com/tuna-os/corral/pkg/kubevirt"
)

// Autostart/shutdown windows — the GUI half of the schedule plugin. Two
// CronJobs flip the VM's runStrategy (Always/Halted) at start/stop boundaries
// (e.g. dev VMs only during business hours). Like snapsched, the artifacts are
// labeled CronJobs the web server manages directly via pkg/cronops, so no
// plugin binary is required on the server.

const powerLabel = "corral.dev/schedule"

func startJobName(vm string) string { return "corral-start-" + vm }
func stopJobName(vm string) string  { return "corral-stop-" + vm }

// GET /api/vms/{ns}/{name}/powerschedule → {start, stop} cron exprs (or "")
func handleGetPowerSchedule(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	cronSchedule := func(job string) string {
		out, err := defaultRunner.Run("kubectl", "get", "cronjob", job, "-n", ns,
			"-o", "jsonpath={.spec.schedule}")
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	jsonResp(w, http.StatusOK, map[string]string{
		"start": cronSchedule(startJobName(name)),
		"stop":  cronSchedule(stopJobName(name)),
	})
}

// POST /api/vms/{ns}/{name}/powerschedule  body: {start, stop} (5-field cron;
// empty clears that boundary). At least one must be set.
func handleSetPowerSchedule(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		Start string `json:"start"`
		Stop  string `json:"stop"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	b.Start, b.Stop = strings.TrimSpace(b.Start), strings.TrimSpace(b.Stop)
	for _, c := range []string{b.Start, b.Stop} {
		if c != "" && len(strings.Fields(c)) != 5 {
			errResp(w, http.StatusBadRequest, fmt.Errorf("%q is not a 5-field cron expression (e.g. \"0 9 * * 1-5\")", c))
			return
		}
	}
	if b.Start == "" && b.Stop == "" {
		errResp(w, http.StatusBadRequest, fmt.Errorf("set at least one of start/stop (5-field cron)"))
		return
	}

	done := taskBegin("power schedule", ns+"/"+name)
	objs := []map[string]any{cronops.ServiceAccount(ns), cronops.Role(ns), cronops.RoleBinding(ns)}
	if b.Start != "" {
		objs = append(objs, cronops.CronJob(startJobName(name), ns, b.Start,
			cronops.PowerScript(name, ns, true), map[string]string{powerLabel: name}))
	}
	if b.Stop != "" {
		objs = append(objs, cronops.CronJob(stopJobName(name), ns, b.Stop,
			cronops.PowerScript(name, ns, false), map[string]string{powerLabel: name}))
	}
	for _, obj := range objs {
		if err := kubevirt.Apply(obj); err != nil {
			done(err)
			errResp(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Clear a boundary the user blanked out.
	if b.Start == "" {
		defaultRunner.Run("kubectl", "delete", "cronjob", startJobName(name), "-n", ns, "--ignore-not-found")
	}
	if b.Stop == "" {
		defaultRunner.Run("kubectl", "delete", "cronjob", stopJobName(name), "-n", ns, "--ignore-not-found")
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"start": b.Start, "stop": b.Stop})
}

// DELETE /api/vms/{ns}/{name}/powerschedule — removes both boundaries.
func handleDeletePowerSchedule(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	done := taskBegin("remove power schedule", ns+"/"+name)
	defaultRunner.Run("kubectl", "delete", "cronjob", startJobName(name), "-n", ns, "--ignore-not-found")
	_, err := defaultRunner.Run("kubectl", "delete", "cronjob", stopJobName(name), "-n", ns, "--ignore-not-found")
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "removed"})
}
