package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A plugin that adds UI is any executable honouring the contract. Each item's
// command prints one document; "calls" records how often corral asked.
const fakeUIPlugin = `#!/bin/sh
dir="$(dirname "$0")"
case "$1" in
  --corral-plugin-metadata)
    echo '{"api":"corral.plugin/v1","name":"fakeui","version":"0","ui":{
      "widgets":[{"id":"spend","title":"Spend","command":"ui spend","refresh":"1s"}],
      "sections":[
        {"id":"report","title":"Report","command":"ui report"},
        {"id":"html","title":"Markup","command":"ui html"},
        {"id":"flood","title":"Flood","command":"ui flood"},
        {"id":"slow","title":"Slow","command":"ui slow"},
        {"id":"crash","title":"Crash","command":"ui crash"},
        {"id":"env","title":"Env","command":"ui env"}]}}' ;;
  ui)
    echo "$2" >> "$dir/calls"
    case "$2" in
      spend)  echo '{"kind":"rows","rows":[{"label":"Month","value":"$12","state":"ok"}]}' ;;
      report) sleep 0.3; echo '{"kind":"message","text":"all good"}' ;;
      html)   echo '{"kind":"html","html":"<script>alert(1)</script>"}' ;;
      flood)  while :; do echo 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; done ;;
      slow)   sleep 5; echo '{"kind":"message","text":"late"}' ;;
      crash)  echo 'token expired' >&2; exit 1 ;;
      env)    printf '{"kind":"message","text":"%s"}' "$CORRAL_PLUGIN" ;;
    esac ;;
esac
`

func setupUIPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "corral-fakeui"), []byte(fakeUIPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	// A plugin whose declaration breaks the contract is reported, not shown.
	bad := `#!/bin/sh
echo '{"api":"corral.plugin/v1","name":"badui","version":"0","ui":{"widgets":[{"id":"Not An ID","title":"x","command":"ui x"}]}}'
`
	if err := os.WriteFile(filepath.Join(dir, "corral-badui"), []byte(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	// A plugin that adds no UI is not listed at all.
	other := "#!/bin/sh\necho '{\"api\":\"corral.plugin/v1\",\"name\":\"other\",\"version\":\"0\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "corral-other"), []byte(other), 0o755); err != nil {
		t.Fatal(err)
	}
	uiMetaMu.Lock()
	uiMetaCache = map[string]uiSource{}
	uiMetaMu.Unlock()
	uiDocMu.Lock()
	uiDocCache = map[string]*uiCacheEntry{}
	uiDocMu.Unlock()
	t.Cleanup(func() {
		uiDocMu.Lock()
		uiDocCache = map[string]*uiCacheEntry{}
		uiDocMu.Unlock()
	})
	return dir
}

func uiItemMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/plugins/ui", handlePluginUI)
	mux.HandleFunc("GET /api/plugins/{plugin}/ui/{id}", handlePluginUIItem)
	return mux
}

func getUI(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	uiItemMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func uiCalls(dir string) []string {
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return strings.Fields(string(b))
}

func TestPluginUIListsDeclarationsWithoutCommands(t *testing.T) {
	setupUIPlugin(t)
	rec := getUI(t, "/api/plugins/ui")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "command") {
		t.Fatalf("the browser was told a command: %s", rec.Body)
	}
	var resp pluginUIResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Plugins) != 1 || resp.Plugins[0].Name != "fakeui" {
		t.Fatalf("plugins = %+v", resp.Plugins)
	}
	p := resp.Plugins[0]
	if len(p.Widgets) != 1 || len(p.Sections) != 6 {
		t.Fatalf("widgets %d, sections %d", len(p.Widgets), len(p.Sections))
	}
	// A declared refresh below the floor is raised to it.
	if got := time.Duration(p.Widgets[0].RefreshMS) * time.Millisecond; got != 15*time.Second {
		t.Fatalf("refresh = %s, want the 15s floor", got)
	}
	if !strings.Contains(resp.Errors["badui"], "lower case") {
		t.Fatalf("errors = %v, want badui refused", resp.Errors)
	}
}

func TestPluginUIItemReturnsTheCheckedDocument(t *testing.T) {
	setupUIPlugin(t)
	rec := getUI(t, "/api/plugins/fakeui/ui/spend")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Document struct {
			Kind string `json:"kind"`
			Rows []struct{ Label, Value, State string }
		} `json:"document"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Document.Kind != "rows" || len(resp.Document.Rows) != 1 || resp.Document.Rows[0].Value != "$12" {
		t.Fatalf("document = %+v", resp.Document)
	}
}

func TestPluginUIItemRefusals(t *testing.T) {
	setupUIPlugin(t)
	old := pluginUITimeout
	pluginUITimeout = 500 * time.Millisecond
	t.Cleanup(func() { pluginUITimeout = old })

	for _, tc := range []struct {
		path string
		code int
		want string
	}{
		// A shape corral cannot draw is refused, never passed through.
		{"/api/plugins/fakeui/ui/html", http.StatusBadGateway, "kind"},
		// A plugin that prints without end is stopped, not read to the end.
		{"/api/plugins/fakeui/ui/flood", http.StatusBadGateway, "more than"},
		{"/api/plugins/fakeui/ui/slow", http.StatusGatewayTimeout, "did not answer"},
		// The plugin's own complaint reaches the operator.
		{"/api/plugins/fakeui/ui/crash", http.StatusBadGateway, "token expired"},
		// A request names an item; it can never name a command.
		{"/api/plugins/fakeui/ui/nope", http.StatusNotFound, "no plugin"},
		{"/api/plugins/fakeui/ui/ui%20html", http.StatusNotFound, "no plugin"},
		{"/api/plugins/other/ui/spend", http.StatusNotFound, "no plugin"},
		{"/api/plugins/badui/ui/x", http.StatusNotFound, "no plugin"},
	} {
		start := time.Now()
		rec := getUI(t, tc.path)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: status %d body %s, want %d containing %q", tc.path, rec.Code, rec.Body, tc.code, tc.want)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("%s took %s; a bad plugin must not hold the request", tc.path, took)
		}
	}
}

func TestPluginUIItemTellsThePluginWhoItIs(t *testing.T) {
	setupUIPlugin(t)
	rec := getUI(t, "/api/plugins/fakeui/ui/env")
	if !strings.Contains(rec.Body.String(), `"text":"fakeui"`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestPluginUIDocumentsAreSharedBetweenViewers(t *testing.T) {
	dir := setupUIPlugin(t)
	now := time.Now()
	pluginUIClock = func() time.Time { return now }
	t.Cleanup(func() { pluginUIClock = time.Now })

	// Five viewers at once make one call.
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := getUI(t, "/api/plugins/fakeui/ui/report"); rec.Code != http.StatusOK {
				t.Errorf("status %d: %s", rec.Code, rec.Body)
			}
		}()
	}
	wg.Wait()
	if calls := uiCalls(dir); len(calls) != 1 {
		t.Fatalf("calls = %v, want one for five concurrent viewers", calls)
	}

	// A viewer arriving inside the window is answered from the cache.
	getUI(t, "/api/plugins/fakeui/ui/report")
	if calls := uiCalls(dir); len(calls) != 1 {
		t.Fatalf("calls = %v, want the cached answer", calls)
	}

	// Once the window has passed, corral asks again.
	now = now.Add(16 * time.Second)
	getUI(t, "/api/plugins/fakeui/ui/report")
	if calls := uiCalls(dir); len(calls) != 2 {
		t.Fatalf("calls = %v, want a fresh call after the window", calls)
	}

	// A failure is kept only briefly, so "Try again" reaches the plugin.
	getUI(t, "/api/plugins/fakeui/ui/crash")
	getUI(t, "/api/plugins/fakeui/ui/crash")
	now = now.Add(4 * time.Second)
	getUI(t, "/api/plugins/fakeui/ui/crash")
	if calls := uiCalls(dir); strings.Count(strings.Join(calls, " "), "crash") != 2 {
		t.Fatalf("calls = %v, want one crash call, then another after the short window", calls)
	}
}

func TestPluginUIDemoFixtureEscapesNothingItself(t *testing.T) {
	// The fixture's markup row has to reach the browser as a plain string,
	// so the browser suite can prove the page shows it as text.
	doc, err := demoPluginUIDocument("spend")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Rows) != 3 || !strings.HasPrefix(doc.Rows[2].Value, "<img") {
		t.Fatalf("rows = %+v", doc.Rows)
	}
}
