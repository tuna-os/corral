package plugin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateSources points the marketplace sources file and the plugin dir at
// temp dirs and clears the env overrides, so Sources() reads only what the
// test wrote. Without this, a developer's ~/.config/corral/marketplaces.json
// would leak into the result.
func isolateSources(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CORRAL_PLUGIN_DIR", t.TempDir())
	t.Setenv("CORRAL_MARKETPLACE_URLS", "")
	t.Setenv("CORRAL_MARKETPLACE_URL", "")
}

func sourceNames(sources []Source) []string {
	var out []string
	for _, s := range sources {
		out = append(out, s.Name)
	}
	return out
}

func findSource(sources []Source, name string) (Source, bool) {
	for _, s := range sources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

func TestSources_EnvListOverridesFileAndSplitsOnSeparators(t *testing.T) {
	isolateSources(t)
	if err := AddSource(Source{Name: "team", URL: "https://team.example/index.json"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORRAL_MARKETPLACE_URLS", " https://a.example/i.json ;https://b.example/i.json,\n,https://c.example/i.json ")
	got := Sources()
	want := []string{"https://a.example/i.json", "https://b.example/i.json", "https://c.example/i.json"}
	if len(got) != len(want) {
		t.Fatalf("Sources() = %+v, want %d env sources only", got, len(want))
	}
	for i, s := range got {
		if s.URL != want[i] || !s.Enabled || !strings.HasPrefix(s.Name, "env-") {
			t.Errorf("source %d = %+v, want enabled env source for %s", i, s, want[i])
		}
	}
}

func TestSources_CorruptFileFallsBackToBuiltin(t *testing.T) {
	isolateSources(t)
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcesPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Sources()
	if len(got) != 1 || got[0].Name != "corral" || got[0].URL != DefaultMarketplaceURL {
		t.Fatalf("Sources() with corrupt file = %+v, want only the built-in", got)
	}
}

func TestAddSource_PersistsAfterBuiltin(t *testing.T) {
	isolateSources(t)
	if err := AddSource(Source{Name: "team", URL: "https://team.example/index.json", AllowUnverified: true}); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	got := Sources()
	if names := sourceNames(got); len(names) != 2 || names[0] != "corral" || names[1] != "team" {
		t.Fatalf("Sources() names = %v, want [corral team]", names)
	}
	team := got[1]
	if !team.Enabled || !team.AllowUnverified || team.URL != "https://team.example/index.json" {
		t.Errorf("persisted source = %+v, want enabled, unverified opt-in kept", team)
	}
	// The built-in is implicit: writing it to the file would duplicate it on
	// the next read.
	data, err := os.ReadFile(sourcesPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), DefaultMarketplaceURL) {
		t.Errorf("sources file stores the built-in marketplace:\n%s", data)
	}
	if info, err := os.Stat(sourcesPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("sources file mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}
}

func TestAddSource_EnabledEvenIfCallerSaysDisabled(t *testing.T) {
	isolateSources(t)
	if err := AddSource(Source{Name: "team", URL: "https://team.example/i.json", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if s, _ := findSource(Sources(), "team"); !s.Enabled {
		t.Errorf("a newly added source should be enabled, got %+v", s)
	}
}

func TestAddSource_Rejects(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		want string
	}{
		{"bad name", Source{Name: "Team", URL: "https://x.example/i.json"}, "invalid marketplace name"},
		{"path in name", Source{Name: "../x", URL: "https://x.example/i.json"}, "invalid marketplace name"},
		{"plain http", Source{Name: "team", URL: "http://x.example/i.json"}, "HTTPS"},
		{"no host", Source{Name: "team", URL: "https:///i.json"}, "invalid marketplace URL"},
		{"reserved builtin name", Source{Name: "corral", URL: "https://evil.example/i.json"}, "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateSources(t)
			err := AddSource(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AddSource(%+v) = %v, want error containing %q", tc.src, err, tc.want)
			}
			if _, err := os.Stat(sourcesPath()); !os.IsNotExist(err) {
				t.Errorf("a rejected source must not write the sources file (stat err %v)", err)
			}
		})
	}
}

func TestAddSource_LoopbackHTTPAllowed(t *testing.T) {
	isolateSources(t)
	for i, u := range []string{"http://127.0.0.1:8080/i.json", "http://localhost/i.json"} {
		if err := AddSource(Source{Name: []string{"one", "two"}[i], URL: u}); err != nil {
			t.Errorf("AddSource(%s): %v — loopback HTTP is the documented dev exception", u, err)
		}
	}
}

func TestAddSource_DuplicateName(t *testing.T) {
	isolateSources(t)
	if err := AddSource(Source{Name: "team", URL: "https://a.example/i.json"}); err != nil {
		t.Fatal(err)
	}
	err := AddSource(Source{Name: "team", URL: "https://b.example/i.json"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second AddSource = %v, want already exists", err)
	}
	if s, _ := findSource(Sources(), "team"); s.URL != "https://a.example/i.json" {
		t.Errorf("duplicate add overwrote the first source: %+v", s)
	}
}

func TestRemoveSource(t *testing.T) {
	isolateSources(t)
	for _, n := range []string{"one", "two"} {
		if err := AddSource(Source{Name: n, URL: "https://" + n + ".example/i.json"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveSource("one"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if names := sourceNames(Sources()); len(names) != 2 || names[0] != "corral" || names[1] != "two" {
		t.Errorf("after remove, names = %v, want [corral two]", names)
	}
	if err := RemoveSource("one"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("removing a missing source = %v, want not found", err)
	}
	if err := RemoveSource("corral"); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Errorf("removing the built-in = %v, want refusal", err)
	}
}

func TestSetSourceEnabled(t *testing.T) {
	isolateSources(t)
	if err := AddSource(Source{Name: "team", URL: "https://team.example/i.json"}); err != nil {
		t.Fatal(err)
	}
	if err := SetSourceEnabled("team", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if s, _ := findSource(Sources(), "team"); s.Enabled {
		t.Errorf("source still enabled after disable: %+v", s)
	}
	if err := SetSourceEnabled("team", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if s, _ := findSource(Sources(), "team"); !s.Enabled {
		t.Errorf("source still disabled after enable: %+v", s)
	}
	if err := SetSourceEnabled("ghost", true); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("enabling a missing source = %v, want not found", err)
	}
	if err := SetSourceEnabled("corral", false); err == nil || !strings.Contains(err.Error(), "always enabled") {
		t.Errorf("disabling the built-in = %v, want refusal", err)
	}
}

func writeIndexFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateIndexFile(t *testing.T) {
	sha := strings.Repeat("a", 64)
	valid := v2Index("bootc", "https://example.com/bootc", sha)
	twoSame := strings.Replace(valid, `"plugins":[{`, `"plugins":[`+pluginJSON("bootc", sha)+`,{`, 1)
	cases := []struct {
		name string
		body string
		want string // "" = valid
	}{
		{"valid v2", valid, ""},
		{"v1 schema", strings.Replace(valid, MarketplaceAPIV2, "corral.marketplace/v1", 1), "schemaVersion"},
		{"no publisher", strings.Replace(valid, `"publisher":{"name":"tuna-os"},"plugins"`, `"plugins"`, 1), "publisher"},
		{"unknown field", strings.Replace(valid, `"name":"test marketplace"`, `"name":"test marketplace","extra":1`, 1), "unknown field"},
		{"duplicate plugin", twoSame, "duplicate plugin"},
		{"invalid entry", strings.Replace(valid, `"version":"0.1.0"`, `"version":"not-semver"`, 1), "invalid version"},
		{"not json", "nope", "invalid character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIndexFile(writeIndexFile(t, tc.body))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateIndexFile: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateIndexFile = %v, want error containing %q", err, tc.want)
			}
		})
	}
	if err := ValidateIndexFile(filepath.Join(t.TempDir(), "missing.json")); !os.IsNotExist(err) {
		t.Errorf("missing file = %v, want not-exist", err)
	}
}

// pluginJSON is one v2 plugin entry, matching what v2Index embeds.
func pluginJSON(name, sha string) string {
	return `{"name":"` + name + `","description":"test plugin","version":"0.1.0","license":"Apache-2.0",` +
		`"publisher":{"name":"tuna-os"},"supportedBackends":["all"],` +
		`"platforms":{"` + platformKey() + `":{"url":"https://example.com/` + name + `","sha256":"` + sha + `"}}}`
}

func indexServer(t *testing.T, plugins ...string) *httptest.Server {
	t.Helper()
	sha := strings.Repeat("b", 64)
	var entries []string
	for _, p := range plugins {
		entries = append(entries, pluginJSON(p, sha))
	}
	body := `{"schemaVersion":"` + MarketplaceAPIV2 + `","name":"m","publisher":{"name":"tuna-os"},"plugins":[` + strings.Join(entries, ",") + `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The env list replaces the built-in source, so these FetchAll tests never
// reach the real marketplace on the network.
func TestFetchAll_MergesSortsAndTagsSource(t *testing.T) {
	isolateSources(t)
	a := indexServer(t, "zeta", "alpha")
	b := indexServer(t, "mid")
	t.Setenv("CORRAL_MARKETPLACE_URLS", a.URL+","+b.URL)

	idx, errs := FetchAll()
	if len(errs) != 0 {
		t.Fatalf("FetchAll errs = %v", errs)
	}
	var names, sources []string
	for _, e := range idx.Plugins {
		names = append(names, e.Name)
		sources = append(sources, e.Source)
	}
	if strings.Join(names, ",") != "alpha,mid,zeta" {
		t.Errorf("merged names = %v, want sorted alpha,mid,zeta", names)
	}
	if strings.Join(sources, ",") != "env-1,env-2,env-1" {
		t.Errorf("entry sources = %v, want each tagged with the source it came from", sources)
	}
	if idx.SchemaVersion != MarketplaceAPIV2 {
		t.Errorf("merged schemaVersion = %q", idx.SchemaVersion)
	}
}

func TestFetchAll_OneBadSourceDoesNotHideTheOthers(t *testing.T) {
	isolateSources(t)
	good := indexServer(t, "bootc")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	t.Setenv("CORRAL_MARKETPLACE_URLS", bad.URL+","+good.URL)

	idx, errs := FetchAll()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "env-1") || !strings.Contains(errs[0].Error(), "500") {
		t.Errorf("errs = %v, want one error naming env-1 and the status", errs)
	}
	if idx.Find("bootc") == nil {
		t.Errorf("good source's plugin missing from merged index: %+v", idx.Plugins)
	}
}

func TestFetchAll_SameNameFromTwoSourcesIsFlaggedAndAmbiguous(t *testing.T) {
	isolateSources(t)
	a := indexServer(t, "bootc")
	b := indexServer(t, "bootc")
	t.Setenv("CORRAL_MARKETPLACE_URLS", a.URL+","+b.URL)

	idx, errs := FetchAll()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "published by both env-1 and env-2") {
		t.Fatalf("errs = %v, want a both-sources conflict warning", errs)
	}
	if idx.Find("bootc") != nil {
		t.Error("an unqualified name published by two sources must not resolve to either")
	}
	if e := idx.Find("env-2/bootc"); e == nil || e.Source != "env-2" {
		t.Errorf("Find(env-2/bootc) = %+v, want the env-2 entry", e)
	}
}

func TestSetPinned(t *testing.T) {
	isolateSources(t)
	if err := SetPinned("bootc", true); err == nil || !strings.Contains(err.Error(), "no installed-state manifest") {
		t.Fatalf("SetPinned on an uninstalled plugin = %v, want manifest error", err)
	}
	if err := writeState(InstalledState{Name: "bootc", Version: "1.2.3", SHA256: "abc"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPinned("bootc", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	st, err := ReadState("bootc")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Pinned || st.Version != "1.2.3" || st.SHA256 != "abc" {
		t.Errorf("state after pin = %+v, want pinned with version/sha kept", st)
	}
	if err := SetPinned("bootc", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReadState("bootc"); st.Pinned {
		t.Error("unpin did not clear Pinned")
	}
}
