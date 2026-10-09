package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func captureStdout(t *testing.T, fn func() bool) (bool, string) {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	result := fn()

	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading pipe: %v", err)
	}
	return result, buf.String()
}

func TestHandleMetadata_NoArgs(t *testing.T) {
	withArgs(t, []string{"corral-test"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "test"})
		})
		if ok {
			t.Fatal("expected HandleMetadata to return false with no flag")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleMetadata_TooManyArgs(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata", "extra"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "test"})
		})
		if ok {
			t.Fatal("expected HandleMetadata to return false with extra args")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleMetadata_WrongFlag(t *testing.T) {
	withArgs(t, []string{"corral-test", "--help"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "test"})
		})
		if ok {
			t.Fatal("expected HandleMetadata to return false for unrelated flag")
		}
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})
}

func TestHandleMetadata_DefaultsAPI(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{
				Name:              "bootc",
				Version:           "0.3.0",
				Description:       "Build bootable container images into VMs",
				Capabilities:      []string{"cli-command", "backend-workflow"},
				SupportedBackends: []string{"kubevirt"},
			})
		})
		if !ok {
			t.Fatal("expected HandleMetadata to return true for the metadata flag")
		}

		var got Metadata
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not valid JSON metadata: %v\noutput: %s", err, out)
		}
		if got.API != API {
			t.Errorf("API = %q, want default %q", got.API, API)
		}
		if got.Name != "bootc" {
			t.Errorf("Name = %q, want %q", got.Name, "bootc")
		}
		if len(got.Capabilities) != 2 {
			t.Errorf("Capabilities = %v, want 2 entries", got.Capabilities)
		}
	})
}

func TestHandleMetadata_PreservesExplicitAPI(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata"}, func() {
		ok, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{API: "corral.plugin/v2", Name: "custom"})
		})
		if !ok {
			t.Fatal("expected HandleMetadata to return true")
		}
		var got Metadata
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not valid JSON metadata: %v", err)
		}
		if got.API != "corral.plugin/v2" {
			t.Errorf("API = %q, want explicit %q to be preserved", got.API, "corral.plugin/v2")
		}
	})
}

func TestHandleMetadata_OmitsEmptyOptionalFields(t *testing.T) {
	withArgs(t, []string{"corral-test", "--corral-plugin-metadata"}, func() {
		_, out := captureStdout(t, func() bool {
			return HandleMetadata(Metadata{Name: "minimal", Version: "0.0.1"})
		})
		var raw map[string]any
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		for _, field := range []string{"description", "capabilities", "permissions", "supportedBackends"} {
			if _, present := raw[field]; present {
				t.Errorf("expected omitempty field %q to be absent, got %v", field, raw[field])
			}
		}
	})
}

// The document parser is the boundary between a plugin's output and the web
// UI, so it is tested against what a careless or hostile plugin would send.

func TestParseDocumentAcceptsEachKind(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"rows","rows":[{"label":"This month","value":"$41.20","state":"ok"}]}`,
		`{"kind":"table","columns":["Host","State"],"rows":[["i-0abc","stopped"]]}`,
		`{"kind":"message","text":"No instances carry the corral tag."}`,
		`{"kind":"rows","rows":[]}`,
	} {
		if _, err := ParseDocument([]byte(raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestParseDocumentRefusesWhatCorralCannotDraw(t *testing.T) {
	long := strings.Repeat("x", MaxDocumentString+1)
	many := "[" + strings.TrimSuffix(strings.Repeat(`["a"],`, MaxDocumentRows+1), ",") + "]"
	cases := map[string]string{
		"not JSON":                `<script>alert(1)</script>`,
		"no kind":                 `{"rows":[]}`,
		"unknown kind":            `{"kind":"html","html":"<b>hi</b>"}`,
		"a chart":                 `{"kind":"chart","series":[1,2,3]}`,
		"rows of the wrong shape": `{"kind":"rows","rows":[["a","b"]]}`,
		"an unknown state":        `{"kind":"rows","rows":[{"label":"a","value":"b","state":"<i>"}]}`,
		"an overlong label":       `{"kind":"rows","rows":[{"label":"` + long + `","value":"b"}]}`,
		"a table with no columns": `{"kind":"table","columns":[],"rows":[]}`,
		"a ragged table":          `{"kind":"table","columns":["A","B"],"rows":[["only one"]]}`,
		"too many rows":           `{"kind":"table","columns":["A"],"rows":` + many + `}`,
		"an empty message":        `{"kind":"message","text":"   "}`,
		"a misnamed field":        `{"kind":"table","columns":["A"],"cells":[["a"]]}`,
		"a row with an action":    `{"kind":"rows","rows":[{"label":"a","value":"b","action":"rm -rf"}]}`,
		"two documents":           `{"kind":"message","text":"a"} {"kind":"message","text":"b"}`,
	}
	for name, raw := range cases {
		if _, err := ParseDocument([]byte(raw)); err == nil {
			t.Errorf("%s was accepted: %s", name, raw)
		}
	}
	big := make([]byte, MaxDocumentBytes+1)
	if _, err := ParseDocument(big); err == nil {
		t.Error("a document over the size limit was accepted")
	}
}

// Markup in a string is just text. The parser does not strip it - corral
// escapes on the way into the page - but it must not be refused either, or a
// value that happens to contain "<" could never be shown.
func TestParseDocumentKeepsMarkupAsText(t *testing.T) {
	doc, err := ParseDocument([]byte(`{"kind":"rows","rows":[{"label":"<b>x</b>","value":"<script>y</script>"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Rows[0].Value != "<script>y</script>" {
		t.Errorf("value changed in parsing: %q", doc.Rows[0].Value)
	}
}

func TestDocumentRoundTripsThroughJSON(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"rows","rows":[{"label":"a","value":"b","state":"warn"}]}`,
		`{"kind":"table","columns":["A","B"],"rows":[["1","2"]]}`,
		`{"kind":"message","text":"hello"}`,
	} {
		doc, err := ParseDocument([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseDocument(out)
		if err != nil {
			t.Fatalf("corral's own output did not parse: %s: %v", out, err)
		}
		if again.Kind != doc.Kind {
			t.Errorf("kind changed: %s -> %s", doc.Kind, again.Kind)
		}
	}
}

func TestUIValidate(t *testing.T) {
	ok := &UI{
		Widgets:  []UIItem{{ID: "spend", Title: "Spend", Command: "ui-widget spend", Refresh: "60s"}},
		Sections: []UIItem{{ID: "hosts", Title: "Hosts", Command: "ui-section hosts"}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid declaration was refused: %v", err)
	}
	var none *UI
	if err := none.Validate(); err != nil {
		t.Fatalf("a plugin with no ui was refused: %v", err)
	}
	bad := map[string]*UI{
		"an id with capitals": {Widgets: []UIItem{{ID: "Spend", Title: "S", Command: "x"}}},
		"an id with a slash":  {Widgets: []UIItem{{ID: "a/b", Title: "S", Command: "x"}}},
		"no title":            {Widgets: []UIItem{{ID: "a", Command: "x"}}},
		"no command":          {Widgets: []UIItem{{ID: "a", Title: "A", Command: "  "}}},
		"a bad refresh":       {Widgets: []UIItem{{ID: "a", Title: "A", Command: "x", Refresh: "soon"}}},
		"a repeated id": {
			Widgets:  []UIItem{{ID: "a", Title: "A", Command: "x"}},
			Sections: []UIItem{{ID: "a", Title: "A", Command: "y"}},
		},
	}
	for name, ui := range bad {
		if err := ui.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	tooMany := &UI{}
	for i := 0; i <= MaxUIItems; i++ {
		tooMany.Widgets = append(tooMany.Widgets, UIItem{ID: fmt.Sprintf("w%d", i), Title: "W", Command: "x"})
	}
	if err := tooMany.Validate(); err == nil {
		t.Error("more items than the limit were accepted")
	}
}

func TestRefreshIntervalHasAFloor(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "1s": MinUIRefresh, "60s": time.Minute, "nonsense": 0, "-5s": 0}
	for in, want := range cases {
		if got := (UIItem{Refresh: in}).RefreshInterval(); got != want {
			t.Errorf("refresh %q = %v, want %v", in, got, want)
		}
	}
}
