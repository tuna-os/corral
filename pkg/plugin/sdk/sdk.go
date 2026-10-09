// Package sdk contains the stable, dependency-light contract implemented by
// Corral extension executables. External authors may emit the same JSON
// without importing this package.
package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const API = "corral.plugin/v1"

type Metadata struct {
	API               string   `json:"api"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Description       string   `json:"description,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	Permissions       []string `json:"permissions,omitempty"`
	SupportedBackends []string `json:"supportedBackends,omitempty"`
	// UI declares what the plugin adds to the web UI (RFC-0002). Nil for a
	// plugin that adds nothing there, which is every plugin before this field.
	UI *UI `json:"ui,omitempty"`
}

// HandleMetadata implements the reserved --corral-plugin-metadata handshake.
// Call it at the beginning of main and return when it reports true.
func HandleMetadata(meta Metadata) bool {
	if len(os.Args) != 2 || os.Args[1] != "--corral-plugin-metadata" {
		return false
	}
	if meta.API == "" {
		meta.API = API
	}
	if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return true
}

// CapHostPower is the capability a plugin declares to power the machines
// that host VMs on and off: an on-demand cloud instance kept stopped when
// idle, a lab box behind a smart plug, a Wake-on-LAN workstation. Core
// knows nothing about any of those; it only calls the plugin:
//
//	corral-<name> host-power list            → JSON []Host on stdout
//	corral-<name> host-power start <host-id> → exit 0 when the request is accepted
//	corral-<name> host-power stop  <host-id> → exit 0 when the request is accepted
//
// start/stop return once the provider has accepted the request; the host
// reaches its new state asynchronously and `list` reports progress.
const CapHostPower = "host-power"

// Host is one power-manageable machine reported by a host-power plugin.
type Host struct {
	// ID is the plugin's own stable identifier, passed back to start/stop.
	ID string `json:"id"`
	// Name is what the UI shows.
	Name string `json:"name"`
	// Node is the Kubernetes node this machine runs, if any, so the UI can
	// tie power state to the node's VMs.
	Node string `json:"node,omitempty"`
	// State is one of running, stopped, starting, stopping, unknown.
	State string `json:"state"`
	// Actions lists what may be requested now ("start", "stop").
	Actions []string `json:"actions,omitempty"`
	// Detail is an optional human-readable note (cost, idle timer, provider state).
	Detail string `json:"detail,omitempty"`
}

// ── Web UI contributions (RFC-0002) ─────────────────────────────────

// UI is what a plugin adds to the web UI. The plugin sends data and corral
// draws it: nothing here carries markup or script, and corral escapes every
// string it shows. That is the whole of the trust model - installing a plugin
// that declares UI does not mean running its code in anyone's browser.
type UI struct {
	// Widgets go on the Datacenter dashboard. They wait in "Add widget" rather
	// than appear on their own: installing a plugin must not rearrange an
	// operator's dashboard.
	Widgets []UIItem `json:"widgets,omitempty"`
	// Sections are screens of their own, reached from the Extensions screen
	// and the command palette. They stay out of the sidebar tree, which is the
	// operator's model of the fleet, not a place for a plugin to add to.
	Sections []UIItem `json:"sections,omitempty"`
}

// UIItem is one widget or section.
type UIItem struct {
	// ID names the item within the plugin: lower case letters, digits and
	// dashes. Corral addresses the item as plugin/id.
	ID    string `json:"id"`
	Title string `json:"title"`
	// Icon is one of corral's own icon names. An unknown name falls back to a
	// default rather than failing, so an icon added later costs nothing.
	Icon string `json:"icon,omitempty"`
	// Command is the arguments corral passes to the plugin to get the item's
	// document, split on whitespace with no shell involved. It comes from the
	// plugin's own metadata and never from a request.
	Command string `json:"command"`
	// Refresh is how often a widget asks again, as a Go duration such as
	// "60s". Corral will not ask more often than MinUIRefresh, whatever this
	// says. Empty means the document is fetched once per view.
	Refresh string `json:"refresh,omitempty"`
}

// Limits corral enforces on what a plugin declares and returns. They are part
// of the contract, so a plugin can be tested against them.
const (
	MaxUIItems        = 8                // widgets plus sections, per plugin
	MinUIRefresh      = 15 * time.Second // the fastest a widget may be polled
	MaxDocumentBytes  = 256 << 10        // the most corral reads from stdout
	MaxDocumentRows   = 200
	MaxDocumentCols   = 12
	MaxDocumentString = 200  // a label, a value or a table cell
	MaxDocumentText   = 2000 // a message
)

var uiIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Validate checks a UI declaration. Corral calls it on the metadata a plugin
// reports and on the marketplace entry that describes it, so a plugin that
// would be refused at run time is refused before anyone installs it.
func (ui *UI) Validate() error {
	if ui == nil {
		return nil
	}
	if n := len(ui.Widgets) + len(ui.Sections); n > MaxUIItems {
		return fmt.Errorf("ui declares %d items; the limit is %d", n, MaxUIItems)
	}
	seen := map[string]bool{}
	check := func(kind string, it UIItem) error {
		if !uiIDPattern.MatchString(it.ID) {
			return fmt.Errorf("ui %s id %q must be lower case letters, digits and dashes", kind, it.ID)
		}
		if seen[it.ID] {
			return fmt.Errorf("ui id %q is used twice", it.ID)
		}
		seen[it.ID] = true
		if strings.TrimSpace(it.Title) == "" || len(it.Title) > 80 {
			return fmt.Errorf("ui %s %q needs a title of 1 to 80 characters", kind, it.ID)
		}
		if len(strings.Fields(it.Command)) == 0 {
			return fmt.Errorf("ui %s %q has no command", kind, it.ID)
		}
		if it.Refresh != "" {
			if _, err := time.ParseDuration(it.Refresh); err != nil {
				return fmt.Errorf("ui %s %q refresh %q is not a duration", kind, it.ID, it.Refresh)
			}
		}
		return nil
	}
	for _, it := range ui.Widgets {
		if err := check("widget", it); err != nil {
			return err
		}
	}
	for _, it := range ui.Sections {
		if err := check("section", it); err != nil {
			return err
		}
	}
	return nil
}

// RefreshInterval is how often corral asks for an item's document: the
// declared interval, never below MinUIRefresh, or zero for "once per view".
func (it UIItem) RefreshInterval() time.Duration {
	if it.Refresh == "" {
		return 0
	}
	d, err := time.ParseDuration(it.Refresh)
	if err != nil || d <= 0 {
		return 0
	}
	if d < MinUIRefresh {
		return MinUIRefresh
	}
	return d
}

// Document is what a plugin prints for a widget or a section: one of a closed
// set of shapes. A kind corral does not know is an error, and the widget says
// so; there is no fallback that guesses.
//
//	{"kind":"rows","rows":[{"label":"This month","value":"$41.20","state":"ok"}]}
//	{"kind":"table","columns":["Host","State"],"rows":[["i-0abc","stopped"]]}
//	{"kind":"message","text":"No instances carry the corral tag."}
type Document struct {
	Kind    string     `json:"kind"`
	Rows    []Row      `json:"-"`
	Columns []string   `json:"columns,omitempty"`
	Cells   [][]string `json:"-"`
	Text    string     `json:"text,omitempty"`
}

// Row is one line of a rows document.
type Row struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// State selects a pill corral already styles: ok, warn, bad or muted.
	State string `json:"state,omitempty"`
}

// MarshalJSON writes the document in the shape ParseDocument reads. The two
// kinds put different things under "rows", so the struct tags alone cannot.
func (d Document) MarshalJSON() ([]byte, error) {
	out := map[string]any{"kind": d.Kind}
	switch d.Kind {
	case "rows":
		out["rows"] = d.Rows
	case "table":
		out["columns"] = d.Columns
		out["rows"] = d.Cells
	case "message":
		out["text"] = d.Text
	}
	return json.Marshal(out)
}

// ParseDocument reads a plugin's output and checks it against the contract.
// It never returns a document corral would refuse to draw: an unknown kind, a
// shape that does not match its kind, or anything over a limit is an error.
func ParseDocument(raw []byte) (Document, error) {
	if len(raw) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("document is %d bytes; the limit is %d", len(raw), MaxDocumentBytes)
	}
	var head struct {
		Kind    string          `json:"kind"`
		Rows    json.RawMessage `json:"rows"`
		Columns []string        `json:"columns"`
		Text    string          `json:"text"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Document{}, fmt.Errorf("document is not JSON: %w", err)
	}
	switch head.Kind {
	case "rows", "table", "message":
	case "":
		return Document{}, fmt.Errorf("document has no kind")
	default:
		return Document{}, fmt.Errorf("document kind %q is not rows, table or message", head.Kind)
	}
	// An unknown field is refused, not ignored. A plugin that writes "cells"
	// for "rows" should fail where its author can see it, not draw an empty
	// table on every operator's screen.
	if err := strictUnmarshal(raw, &head); err != nil {
		return Document{}, fmt.Errorf("%s document does not match the contract: %w", head.Kind, err)
	}
	doc := Document{Kind: head.Kind}
	tooLong := func(s string, max int) bool { return len([]rune(s)) > max }
	switch head.Kind {
	case "rows":
		if err := strictUnmarshal(orEmpty(head.Rows), &doc.Rows); err != nil {
			return Document{}, fmt.Errorf("rows document: rows must be a list of {label, value, state}")
		}
		if len(doc.Rows) > MaxDocumentRows {
			return Document{}, fmt.Errorf("rows document has %d rows; the limit is %d", len(doc.Rows), MaxDocumentRows)
		}
		for i, r := range doc.Rows {
			if tooLong(r.Label, MaxDocumentString) || tooLong(r.Value, MaxDocumentString) {
				return Document{}, fmt.Errorf("rows document: row %d is longer than %d characters", i, MaxDocumentString)
			}
			switch r.State {
			case "", "ok", "warn", "bad", "muted":
			default:
				return Document{}, fmt.Errorf("rows document: row %d state %q is not ok, warn, bad or muted", i, r.State)
			}
		}
	case "table":
		if len(head.Columns) == 0 || len(head.Columns) > MaxDocumentCols {
			return Document{}, fmt.Errorf("table document needs 1 to %d columns", MaxDocumentCols)
		}
		for _, c := range head.Columns {
			if tooLong(c, MaxDocumentString) {
				return Document{}, fmt.Errorf("table document: a column name is longer than %d characters", MaxDocumentString)
			}
		}
		if err := json.Unmarshal(orEmpty(head.Rows), &doc.Cells); err != nil {
			return Document{}, fmt.Errorf("table document: rows must be a list of lists of strings")
		}
		if len(doc.Cells) > MaxDocumentRows {
			return Document{}, fmt.Errorf("table document has %d rows; the limit is %d", len(doc.Cells), MaxDocumentRows)
		}
		for i, row := range doc.Cells {
			if len(row) != len(head.Columns) {
				return Document{}, fmt.Errorf("table document: row %d has %d cells for %d columns", i, len(row), len(head.Columns))
			}
			for _, c := range row {
				if tooLong(c, MaxDocumentString) {
					return Document{}, fmt.Errorf("table document: row %d has a cell longer than %d characters", i, MaxDocumentString)
				}
			}
		}
		doc.Columns = head.Columns
	case "message":
		if strings.TrimSpace(head.Text) == "" {
			return Document{}, fmt.Errorf("message document has no text")
		}
		if tooLong(head.Text, MaxDocumentText) {
			return Document{}, fmt.Errorf("message document is longer than %d characters", MaxDocumentText)
		}
		doc.Text = head.Text
	}
	return doc, nil
}

func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("more than one JSON value")
	}
	return nil
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}
