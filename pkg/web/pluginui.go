package web

// Plugin contributions to the web UI (RFC-0002, stages 1 and 2).
//
// A plugin declares widgets and sections in its metadata. For each one, corral
// runs the command the plugin named, reads one document from stdout, checks it
// against the closed set of shapes in sdk.ParseDocument, and hands the browser
// that checked document - never the plugin's raw output, and never markup. The
// browser escapes every string again on the way into the page. No plugin code
// runs in anyone's browser; that is the trust boundary ADR-0007 is about, and
// everything below exists to hold it.
//
// What this adds beyond the RFC, because the endpoint runs plugin code on a
// GET that every viewer's page repeats:
//
//   - Reads are bounded. A plugin that prints more than sdk.MaxDocumentBytes
//     is stopped and refused, rather than read into memory until a size check.
//   - Each call has a timeout, and a widget that times out says so.
//   - Documents are cached for sdk.MinUIRefresh and shared by every viewer,
//     with one call in flight per item. Ten operators looking at a widget that
//     wraps a cloud API would otherwise make ten calls to it every interval.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

// pluginUITimeout bounds one call. A widget is a glance, not a job: the
// host-power hook allows 30s for a cloud API to start a machine, and a widget
// that has not answered in a third of that is not going to be read.
var pluginUITimeout = 10 * time.Second

// pluginUIItem is what the browser is told about an item. The command stays
// on the server: the browser never needs it, and has no business with it.
type pluginUIItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Icon      string `json:"icon,omitempty"`
	RefreshMS int64  `json:"refresh_ms,omitempty"`
}

type pluginUIEntry struct {
	Name     string         `json:"name"`
	Widgets  []pluginUIItem `json:"widgets,omitempty"`
	Sections []pluginUIItem `json:"sections,omitempty"`
}

type pluginUIResp struct {
	Plugins []pluginUIEntry `json:"plugins"`
	// Errors names plugins whose declaration was refused, so one bad plugin
	// does not hide the rest and its author can see why.
	Errors map[string]string `json:"errors,omitempty"`
}

// A plugin's UI declaration, read from its own metadata. The binary's own
// metadata is the authority at run time, not the marketplace entry: it is the
// binary that implements the commands. The entry's copy is what the operator
// saw before installing.
type uiSource struct {
	name string
	ui   *sdk.UI
	err  error
}

// Cached per binary, keyed by path, size and modification time, so a plugin
// reinstalled in place is read again rather than served from a stale entry.
var (
	uiMetaMu    sync.Mutex
	uiMetaCache = map[string]uiSource{}
)

func uiMetaKey(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return path
	}
	return fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())
}

func pluginUISources() []uiSource {
	var out []uiSource
	for _, p := range plugin.Installed() {
		key := uiMetaKey(p.Path)
		uiMetaMu.Lock()
		src, ok := uiMetaCache[key]
		uiMetaMu.Unlock()
		if !ok {
			src = uiSource{name: p.Name}
			meta, err := plugin.Inspect(p.Name)
			switch {
			case err != nil:
				// A plugin with no metadata adds no UI; that is not an error
				// worth reporting on every page.
				src.ui = nil
			case meta.UI != nil:
				if verr := meta.UI.Validate(); verr != nil {
					src.err = verr
				} else {
					src.ui = meta.UI
				}
			}
			uiMetaMu.Lock()
			uiMetaCache[key] = src
			uiMetaMu.Unlock()
		}
		if src.ui != nil || src.err != nil {
			out = append(out, src)
		}
	}
	if demoMode {
		out = append(out, demoPluginUISource())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func toBrowserItems(items []sdk.UIItem) []pluginUIItem {
	out := make([]pluginUIItem, 0, len(items))
	for _, it := range items {
		out = append(out, pluginUIItem{
			ID: it.ID, Title: it.Title, Icon: it.Icon,
			RefreshMS: it.RefreshInterval().Milliseconds(),
		})
	}
	return out
}

// GET /api/plugins/ui - what each installed plugin adds to the web UI.
func handlePluginUI(w http.ResponseWriter, r *http.Request) {
	resp := pluginUIResp{Plugins: []pluginUIEntry{}}
	for _, src := range pluginUISources() {
		if src.err != nil {
			if resp.Errors == nil {
				resp.Errors = map[string]string{}
			}
			resp.Errors[src.name] = src.err.Error()
			continue
		}
		resp.Plugins = append(resp.Plugins, pluginUIEntry{
			Name:     src.name,
			Widgets:  toBrowserItems(src.ui.Widgets),
			Sections: toBrowserItems(src.ui.Sections),
		})
	}
	jsonResp(w, http.StatusOK, resp)
}

// findUIItem looks an item up by plugin and id. The command it returns comes
// from that plugin's metadata, so a request names an item but can never name
// a command.
func findUIItem(name, id string) (sdk.UIItem, bool) {
	for _, src := range pluginUISources() {
		if src.name != name || src.ui == nil {
			continue
		}
		for _, it := range append(append([]sdk.UIItem{}, src.ui.Widgets...), src.ui.Sections...) {
			if it.ID == id {
				return it, true
			}
		}
	}
	return sdk.UIItem{}, false
}

// cappedBuffer keeps the first max bytes and drops the rest. It never refuses
// a write, which would leave the plugin blocked on a full pipe until the
// timeout; instead it calls stop the first time it overflows, so a plugin
// that prints without end is killed at once rather than read into memory.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
	stop     func()
}

func (c *cappedBuffer) spill() {
	if !c.overflow && c.stop != nil {
		c.stop()
	}
	c.overflow = true
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.spill()
		}
	} else if len(p) > 0 {
		c.spill()
	}
	return len(p), nil
}

var errPluginUITimeout = errors.New("timed out")

// runUIItem runs one item's command and checks what it prints.
func runUIItem(name string, it sdk.UIItem) (sdk.Document, error) {
	bin := plugin.Resolve(name)
	if bin == "" {
		return sdk.Document{}, fmt.Errorf("plugin %q is not installed", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginUITimeout)
	defer cancel()
	// Split on whitespace and exec directly. There is no shell, so nothing in
	// a command is interpreted as anything but an argument.
	cmd := exec.CommandContext(ctx, bin, strings.Fields(it.Command)...)
	cmd.Env = append(os.Environ(), "CORRAL_PLUGIN="+name)
	stdout := &cappedBuffer{max: sdk.MaxDocumentBytes, stop: cancel}
	stderr := &cappedBuffer{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Do not wait on a pipe a killed plugin's children may still hold open.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stdout.overflow {
		return sdk.Document{}, fmt.Errorf("the plugin printed more than %d bytes", sdk.MaxDocumentBytes)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return sdk.Document{}, errPluginUITimeout
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return sdk.Document{}, fmt.Errorf("the plugin failed: %s", msg)
	}
	return sdk.ParseDocument(stdout.buf.Bytes())
}

// One document per item, shared by every viewer for sdk.MinUIRefresh, with a
// single call in flight. An error is kept too, for uiErrorTTL, so a plugin
// that is failing is not asked again by every page at once.
type uiCacheEntry struct {
	doc  sdk.Document
	err  error
	at   time.Time
	busy chan struct{} // closed when the call in flight finishes
}

// uiErrorTTL is how long a failure is kept. It is short so that "Try again"
// in the page reaches the plugin, while a page full of widgets still makes
// one call between them, not one each.
const uiErrorTTL = 3 * time.Second

func (e *uiCacheEntry) ttl() time.Duration {
	if e.err != nil {
		return uiErrorTTL
	}
	return sdk.MinUIRefresh
}

var (
	uiDocMu    sync.Mutex
	uiDocCache = map[string]*uiCacheEntry{}
)

// pluginUIClock is the time source, so a test can age the cache.
var pluginUIClock = time.Now

func cachedUIDocument(name string, it sdk.UIItem, fetch func() (sdk.Document, error)) (sdk.Document, error) {
	key := name + "/" + it.ID
	for {
		uiDocMu.Lock()
		e := uiDocCache[key]
		if e != nil && e.busy == nil && pluginUIClock().Sub(e.at) < e.ttl() {
			uiDocMu.Unlock()
			return e.doc, e.err
		}
		if e != nil && e.busy != nil {
			// Somebody is already asking. Wait for that answer, not a second.
			wait := e.busy
			uiDocMu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		uiDocCache[key] = &uiCacheEntry{busy: done}
		uiDocMu.Unlock()

		doc, err := fetch()

		uiDocMu.Lock()
		uiDocCache[key] = &uiCacheEntry{doc: doc, err: err, at: pluginUIClock()}
		uiDocMu.Unlock()
		close(done)
		return doc, err
	}
}

// GET /api/plugins/{plugin}/ui/{id} - one item's checked document.
func handlePluginUIItem(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("plugin"), r.PathValue("id")
	it, ok := findUIItem(name, id)
	if !ok {
		errResp(w, http.StatusNotFound, fmt.Errorf("no plugin %q declares a ui item %q", name, id))
		return
	}
	fetch := func() (sdk.Document, error) { return runUIItem(name, it) }
	if demoMode && name == demoPluginUIName {
		fetch = func() (sdk.Document, error) { return demoPluginUIDocument(id) }
	}
	doc, err := cachedUIDocument(name, it, fetch)
	switch {
	case errors.Is(err, errPluginUITimeout):
		errResp(w, http.StatusGatewayTimeout, fmt.Errorf("%s did not answer within %s", name, pluginUITimeout))
	case err != nil:
		errResp(w, http.StatusBadGateway, fmt.Errorf("%s: %w", name, err))
	default:
		jsonResp(w, http.StatusOK, map[string]any{"document": doc})
	}
}

// browserUI is a marketplace entry's declaration as the Extensions screen
// shows it before install: titles and kinds, without the commands.
func browserUI(name string, ui *sdk.UI) *pluginUIEntry {
	if ui == nil || ui.Validate() != nil || len(ui.Widgets)+len(ui.Sections) == 0 {
		return nil
	}
	return &pluginUIEntry{Name: name, Widgets: toBrowserItems(ui.Widgets), Sections: toBrowserItems(ui.Sections)}
}
