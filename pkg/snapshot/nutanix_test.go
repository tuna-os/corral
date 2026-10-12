package snapshot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/nutanix"
	"github.com/tuna-os/corral/pkg/types"
)

// fakeNutanixPC answers the v4 calls the Nutanix snapshot adapter makes: one
// running VM, one crash-consistent recovery point, tasks that succeed. The
// shapes mirror pkg/nutanix's own fixtures; this fake only proves the
// adapter drives them, so it stays minimal on purpose.
func fakeNutanixPC(t *testing.T) *nutanix.Client {
	t.Helper()
	const vmID = "11111111-1111-1111-1111-111111111111"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		task := map[string]any{"data": map[string]any{"extId": "task-1"}}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/ahv/config/vms"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []any{map[string]any{
					"extId": vmID, "name": "web-1", "powerState": "ON",
					"numSockets": 1, "numCoresPerSocket": 1, "memorySizeBytes": 2147483648,
				}},
				"metadata": map[string]any{"totalAvailableResults": 1},
			})
		case r.Method == http.MethodGet && strings.Contains(path, "/ahv/config/vms/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"extId": vmID, "name": "web-1", "powerState": "ON"},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/recovery-points"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []any{map[string]any{
					"extId": "rp-1", "name": "nightly",
					"creationTime": "2026-10-03T00:00:00Z",
					"consistency":  "CRASH", "vmExtId": vmID,
				}},
				"metadata": map[string]any{"totalAvailableResults": 1},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/recovery-points"):
			_ = json.NewEncoder(w).Encode(task)
		case r.Method == http.MethodDelete && strings.Contains(path, "/recovery-points/"):
			_ = json.NewEncoder(w).Encode(task)
		case r.Method == http.MethodPost && strings.Contains(path, "/$actions/"):
			_ = json.NewEncoder(w).Encode(task)
		case r.Method == http.MethodGet && strings.Contains(path, "/config/tasks/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"extId": "task-1", "status": "SUCCEEDED"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "no fake handler for " + path})
		}
	}))
	t.Cleanup(srv.Close)
	client, err := nutanix.New(nutanix.Config{Host: srv.URL, Username: "admin", Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := srv.Client()
	httpClient.Timeout = nutanix.Timeout
	return client.WithHTTPClient(httpClient)
}

func TestNutanixSnapshotsEndToEnd(t *testing.T) {
	old := NutanixClientFor
	client := fakeNutanixPC(t)
	NutanixClientFor = func(ref types.InstanceRef) (*nutanix.Client, error) { return client, nil }
	t.Cleanup(func() { NutanixClientFor = old })

	ref := types.InstanceRef{Backend: "nutanix", Context: "pc", Name: "web-1"}
	adapter := Nutanix{}

	created, err := adapter.Create(ref, "nightly")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "nightly" || created.Source != ref.Name || !created.Ready {
		t.Fatalf("bad snapshot: %+v", created)
	}
	// Recovery points are disk-only with no quiesce flag: crash, always —
	// the Filesystem label stays gated behind proof v4 cannot supply.
	if created.Consistency != Crash {
		t.Fatalf("capture consistency = %q, want crash", created.Consistency)
	}

	listed, err := adapter.List(ref)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Consistency != Crash {
		t.Fatalf("listed = %+v", listed)
	}

	if err := adapter.Restore(ref, "nightly"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := adapter.Delete(ref, "nightly"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Deleting what is already gone is not an error — retention races
	// with a human clearing up.
	if err := adapter.Delete(ref, "gone"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}
