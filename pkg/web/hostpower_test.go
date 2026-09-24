package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A host-power plugin is any executable honouring the contract; a shell
// script is enough to prove core has no provider knowledge.
const fakeHostPowerPlugin = `#!/bin/sh
case "$1" in
  --corral-plugin-metadata)
    echo '{"api":"corral.plugin/v1","name":"fakepower","version":"0","capabilities":["host-power"]}' ;;
  host-power)
    case "$2" in
      list) echo '[{"id":"zone/h1","name":"Lab host","node":"n1","state":"stopped","actions":["start"]}]' ;;
      start|stop) echo "$2 $3" >> "$(dirname "$0")/calls" ;;
      *) exit 2 ;;
    esac ;;
esac
`

func setupHostPowerPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "corral-fakepower"), []byte(fakeHostPowerPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	// A plugin without the capability must be ignored.
	other := "#!/bin/sh\necho '{\"api\":\"corral.plugin/v1\",\"name\":\"other\",\"version\":\"0\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "corral-other"), []byte(other), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHostPowerListAggregatesCapablePlugins(t *testing.T) {
	setupHostPowerPlugin(t)
	rec := httptest.NewRecorder()
	handleHostPower(rec, httptest.NewRequest(http.MethodGet, "/api/hostpower", nil))
	var resp hostPowerResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if len(resp.Hosts) != 1 || resp.Hosts[0].Plugin != "fakepower" || resp.Hosts[0].ID != "zone/h1" || resp.Hosts[0].State != "stopped" {
		t.Fatalf("hosts = %+v, errors = %v", resp.Hosts, resp.Errors)
	}
}

func TestHostPowerActionDispatchesToPlugin(t *testing.T) {
	dir := setupHostPowerPlugin(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hostpower/{plugin}/{action}", handleHostPowerAction)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/hostpower/fakepower/start?id=zone%2Fh1", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.TrimSpace(string(calls)) != "start zone/h1" {
		t.Fatalf("plugin calls = %q", calls)
	}

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/hostpower/fakepower/reboot?id=zone%2Fh1", http.StatusBadRequest},
		{"/api/hostpower/fakepower/start?id=--help", http.StatusBadRequest},
		{"/api/hostpower/other/start?id=h1", http.StatusNotFound},
		{"/api/hostpower/missing/start?id=h1", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, nil))
		if rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d (%s)", tc.path, rec.Code, tc.code, rec.Body)
		}
	}
}
