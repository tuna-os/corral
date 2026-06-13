package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanthor/corral/pkg/cronops"
	"github.com/hanthor/corral/pkg/kubevirt"
)

// Snapshot scheduling — the GUI half of the snapsched plugin. The plugin ships
// a CLI, but the actual artifact is a labeled CronJob the cluster runs, so the
// web server manages it directly via pkg/cronops (no plugin binary needed).
// Gated in the UI on caps.plugins containing "snapsched".

const snapschedLabel = "corral.dev/snapsched"

func snapCronName(vm string) string { return "corral-snap-" + vm }

// everyToCron mirrors the snapsched plugin's --every presets.
func everyToCron(every string) (string, error) {
	switch every {
	case "30m":
		return "*/30 * * * *", nil
	case "1h":
		return "0 * * * *", nil
	case "6h":
		return "0 */6 * * *", nil
	case "12h":
		return "0 */12 * * *", nil
	case "24h":
		return "0 3 * * *", nil
	}
	if len(strings.Fields(every)) == 5 {
		return every, nil
	}
	return "", fmt.Errorf("every must be 30m/1h/6h/12h/24h or a 5-field cron expression")
}

// GET /api/vms/{ns}/{name}/snapschedule → {schedule, keep, lastRun} or {}
func handleGetSnapSchedule(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	out, err := defaultRunner.Run("kubectl", "get", "cronjob", snapCronName(name),
		"-n", ns, "-o", "json")
	if err != nil {
		jsonResp(w, http.StatusOK, map[string]any{}) // no schedule
		return
	}
	var cj struct {
		Spec struct {
			Schedule string `json:"schedule"`
			Suspend  bool   `json:"suspend"`
		} `json:"spec"`
		Status struct {
			LastScheduleTime string `json:"lastScheduleTime"`
		} `json:"status"`
	}
	if json.Unmarshal(out, &cj) != nil {
		jsonResp(w, http.StatusOK, map[string]any{})
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"schedule":  cj.Spec.Schedule,
		"suspended": cj.Spec.Suspend,
		"lastRun":   cj.Status.LastScheduleTime,
	})
}

// POST /api/vms/{ns}/{name}/snapschedule  body: {every, keep}
func handleSetSnapSchedule(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		Every string `json:"every"`
		Keep  int    `json:"keep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	cron, err := everyToCron(b.Every)
	if err != nil {
		errResp(w, http.StatusBadRequest, err)
		return
	}
	if b.Keep < 1 {
		b.Keep = 12
	}
	done := taskBegin("snapshot schedule", ns+"/"+name)
	// RBAC (idempotent) + the CronJob.
	for _, obj := range []map[string]any{
		cronops.ServiceAccount(ns),
		cronops.Role(ns),
		cronops.RoleBinding(ns),
		cronops.CronJob(snapCronName(name), ns, cron,
			cronops.SnapshotScript(name, ns, b.Keep),
			map[string]string{snapschedLabel: name}),
	} {
		if err := kubevirt.Apply(obj); err != nil {
			done(err)
			errResp(w, http.StatusInternalServerError, err)
			return
		}
	}
	done(nil)
	jsonResp(w, http.StatusOK, map[string]string{"schedule": cron})
}

// DELETE /api/vms/{ns}/{name}/snapschedule — removes the schedule (snapshots kept)
func handleDeleteSnapSchedule(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	done := taskBegin("remove snapshot schedule", ns+"/"+name)
	_, err := defaultRunner.Run("kubectl", "delete", "cronjob", snapCronName(name),
		"-n", ns, "--ignore-not-found")
	done(err)
	if err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "removed"})
}
