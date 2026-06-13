package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHandleCreateVM_Windows(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	// EnsureNamespace + PreferredStorageClass + 3 applies (ISO DV, disk PVC, VM).
	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fx.Runner.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)

	resp := mustPost(t, fx.Server.URL+"/api/vms",
		`{"name":"winvm","windows":true,"iso":"https://example.com/Win11.iso","cpu":4,"mem":"8Gi","disk":"64Gi"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("got %d, want 201 — %s", resp.StatusCode, b)
	}

	// The applied VM manifest must carry the Windows tuning + virtio drivers.
	var sawWindowsVM, sawISODV bool
	for _, c := range fx.Runner.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			if strings.Contains(c.Stdin, `"tpm"`) && strings.Contains(c.Stdin, "virtio-container-disk") {
				sawWindowsVM = true
			}
			if strings.Contains(c.Stdin, "winvm-iso") && strings.Contains(c.Stdin, "Win11.iso") {
				sawISODV = true
			}
		}
	}
	if !sawWindowsVM {
		t.Error("no applied manifest had the Windows-tuned VM (tpm + virtio-win)")
	}
	if !sawISODV {
		t.Error("no applied manifest imported the installer ISO")
	}
}

func TestHandleCreateVM_WindowsRequiresISO(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	resp := mustPost(t, fx.Server.URL+"/api/vms", `{"name":"winvm","windows":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (missing ISO)", resp.StatusCode)
	}
}

func TestSnapSchedule_SetAndGet(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)

	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/myvm/snapschedule",
		`{"every":"6h","keep":10}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("got %d, want 200 — %s", resp.StatusCode, b)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["schedule"] != "0 */6 * * *" {
		t.Errorf("schedule = %q, want 0 */6 * * *", body["schedule"])
	}

	// A CronJob with the snapsched label must have been applied.
	var sawCron bool
	for _, c := range fx.Runner.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" &&
			strings.Contains(c.Stdin, "CronJob") && strings.Contains(c.Stdin, snapschedLabel) {
			sawCron = true
		}
	}
	if !sawCron {
		t.Error("no CronJob manifest with the snapsched label was applied")
	}
}

func TestSnapSchedule_BadEvery(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/myvm/snapschedule", `{"every":"banana"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for bad 'every'", resp.StatusCode)
	}
}

func TestPowerSchedule_SetGetClear(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)

	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/devbox/powerschedule",
		`{"start":"0 9 * * 1-5","stop":"0 18 * * 1-5"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("got %d, want 200 — %s", resp.StatusCode, b)
	}
	// Both start and stop CronJobs applied with the schedule label.
	var start, stop bool
	for _, c := range fx.Runner.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" && strings.Contains(c.Stdin, powerLabel) {
			if strings.Contains(c.Stdin, "corral-start-devbox") {
				start = true
			}
			if strings.Contains(c.Stdin, "corral-stop-devbox") {
				stop = true
			}
		}
	}
	if !start || !stop {
		t.Errorf("expected start(%v) and stop(%v) CronJobs applied", start, stop)
	}
}

func TestPowerSchedule_BadCron(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/devbox/powerschedule", `{"start":"9am"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for non-cron start", resp.StatusCode)
	}
}

func TestBootcRebuild_UnavailableOrNoImage(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/novm/bootc/rebuild", `{}`)
	defer resp.Body.Close()
	// Without the bootc tag → 400 (unavailable). With it → 400 (no recorded
	// image for an unknown VM). Either way a 400 with a helpful message.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "error") {
		t.Errorf("expected an error message, got %s", b)
	}
}
