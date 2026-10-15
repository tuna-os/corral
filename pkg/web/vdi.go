package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/vdi"
)

// The VDI self-service broker API (#227, ADR-0013). It is off unless
// CORRAL_VDI_BROKER is set. Every route resolves the caller through
// vdiRoute, and the broker authorizes each desktop operation against the
// desktop's claim holder. That is why adminGate lets non-admin POSTs under
// /api/vdi/ through (selfServiceMutation): a route added here must
// authorize itself.

// vdiCluster is the broker's cluster seam; tests install vditest.Cluster.
var vdiCluster = vdi.KubeCluster()

// vdiBroker is the broker routes serves; nil when the broker is off.
var vdiBroker *vdi.Broker

// vdiReclaimInterval is how often the broker applies its reclaim policy.
const vdiReclaimInterval = 30 * time.Second

// newVDIBroker builds the broker from the environment:
//
//	CORRAL_VDI_BROKER            1/true/yes turns the broker on
//	CORRAL_VDI_NAMESPACES        namespaces whose pools it serves (default: corral's default namespace)
//	CORRAL_VDI_DISCONNECT_GRACE  reclaim this long after the last console closes (default 15m; 0 disables)
//	CORRAL_VDI_MAX_SESSION       reclaim a claim this old, connected or not (default 0: no ceiling)
func newVDIBroker() (*vdi.Broker, error) {
	if !envEnabled("CORRAL_VDI_BROKER") {
		return nil, nil
	}
	namespaces := strings.FieldsFunc(os.Getenv("CORRAL_VDI_NAMESPACES"), func(r rune) bool { return r == ',' || r == ' ' })
	if len(namespaces) == 0 {
		namespaces = []string{kubevirt.DefaultNamespace}
	}
	grace, err := envDuration("CORRAL_VDI_DISCONNECT_GRACE", 15*time.Minute)
	if err != nil {
		return nil, err
	}
	maxSession, err := envDuration("CORRAL_VDI_MAX_SESSION", 0)
	if err != nil {
		return nil, err
	}
	return vdi.NewBroker(vdiCluster, vdi.BrokerConfig{
		Namespaces: namespaces, Audit: auditVDI, DisconnectGrace: grace, MaxSession: maxSession,
	}), nil
}

// envDuration parses a Go duration ("15m", "8h", "0") from key, or returns
// def when key is unset.
func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s=%q: want a non-negative duration such as 15m or 8h", key, raw)
	}
	return d, nil
}

// startVDIReclaim runs the broker's reclaim policy until ctx ends.
func startVDIReclaim(ctx context.Context) {
	if b := vdiBroker; b != nil {
		go b.Run(ctx, vdiReclaimInterval, func(err error) {
			fmt.Fprintln(os.Stderr, "vdi reclaim:", err)
		})
	}
}

// trackConsole reports a console session on a VDI desktop to the broker,
// for disconnect reclaim (#228). Consoles of other VMs are ignored.
func trackConsole(h websocket.Handler) websocket.Handler {
	return func(ws *websocket.Conn) {
		if b := vdiBroker; b != nil {
			closed := b.ConsoleOpened(ws.Request().PathValue("ns"), ws.Request().PathValue("name"))
			defer closed()
		}
		h(ws)
	}
}

func envEnabled(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// auditVDI records broker decisions in the task log.
func auditVDI(e vdi.AuditEvent) {
	target := e.Target
	if e.Holder != "" {
		target += " (held by " + e.Holder + ")"
	}
	action := "vdi " + e.Action
	if e.Reason != "" {
		action += " (" + e.Reason + ")"
	}
	taskBegin(e.Actor, action, target)(e.Err)
}

func registerVDIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/vdi/pools", vdiRoute(handleVDIPools))
	mux.HandleFunc("POST /api/vdi/pools/{kind}/{ns}/{pool}/claim", vdiRoute(handleVDIClaim))
	mux.HandleFunc("GET /api/vdi/desktops", vdiRoute(handleVDIDesktops))
	mux.HandleFunc("GET /api/vdi/desktops/{kind}/{ns}/{member}", vdiRoute(handleVDIDesktop))
	mux.HandleFunc("POST /api/vdi/desktops/{kind}/{ns}/{member}/start", vdiRoute(handleVDIStart))
	mux.HandleFunc("POST /api/vdi/desktops/{kind}/{ns}/{member}/release", vdiRoute(handleVDIRelease))
}

type vdiHandler func(w http.ResponseWriter, r *http.Request, b *vdi.Broker, a vdi.Actor)

// vdiRoute resolves the caller of a VDI route. Only an end-user identity
// from the trusted identity headers may use the broker: an anonymous
// caller cannot hold a desktop, and a Corral peer's service identity is
// not a user.
func vdiRoute(h vdiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := vdiBroker
		if b == nil {
			errResp(w, http.StatusNotFound, fmt.Errorf("the VDI broker is off (set CORRAL_VDI_BROKER=1)"))
			return
		}
		if r.Header.Get("Corral-Service-Identity") != "" {
			errResp(w, http.StatusForbidden, fmt.Errorf("a service identity cannot use VDI self-service"))
			return
		}
		login, _ := caller(r)
		if login == "" {
			errResp(w, http.StatusUnauthorized, vdi.ErrUnauthenticated)
			return
		}
		h(w, r, b, vdi.Actor{Identity: login, Admin: isAdmin(r)})
	}
}

func vdiStatus(err error) int {
	switch {
	case errors.Is(err, vdi.ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, vdi.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, vdi.ErrNotFound), errors.Is(err, vdi.ErrPoolNotFound):
		return http.StatusNotFound
	case errors.Is(err, vdi.ErrPoolExhausted), errors.Is(err, vdi.ErrPersistentCTPool):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func memberRef(r *http.Request) vdi.MemberRef {
	return vdi.MemberRef{Kind: vdi.PoolKind(r.PathValue("kind")), Namespace: r.PathValue("ns"), Name: r.PathValue("member")}
}

// GET /api/vdi/pools — the pools the broker serves, with free counts.
func handleVDIPools(w http.ResponseWriter, _ *http.Request, b *vdi.Broker, _ vdi.Actor) {
	pools, err := b.Pools()
	if err != nil {
		errResp(w, vdiStatus(err), err)
		return
	}
	if pools == nil {
		pools = []vdi.PoolStatus{}
	}
	jsonResp(w, http.StatusOK, pools)
}

// POST /api/vdi/pools/{kind}/{ns}/{pool}/claim — claim a desktop for the
// caller. A retry returns the desktop the caller already holds. A desktop
// that failed to start is still returned, with startError set.
func handleVDIClaim(w http.ResponseWriter, r *http.Request, b *vdi.Broker, a vdi.Actor) {
	pool := vdi.PoolRef{Kind: vdi.PoolKind(r.PathValue("kind")), Namespace: r.PathValue("ns"), Name: r.PathValue("pool")}
	d, err := b.Claim(a, pool)
	if err != nil {
		errResp(w, vdiStatus(err), err)
		return
	}
	jsonResp(w, http.StatusOK, d)
}

// GET /api/vdi/desktops[?all=1] — the caller's desktops; all=1 lists every
// user's (admin only).
func handleVDIDesktops(w http.ResponseWriter, r *http.Request, b *vdi.Broker, a vdi.Actor) {
	ds, err := b.Desktops(a, r.URL.Query().Get("all") == "1")
	if err != nil {
		errResp(w, vdiStatus(err), err)
		return
	}
	jsonResp(w, http.StatusOK, ds)
}

// GET /api/vdi/desktops/{kind}/{ns}/{member} — inspect one desktop.
func handleVDIDesktop(w http.ResponseWriter, r *http.Request, b *vdi.Broker, a vdi.Actor) {
	d, err := b.Desktop(a, memberRef(r))
	if err != nil {
		errResp(w, vdiStatus(err), err)
		return
	}
	jsonResp(w, http.StatusOK, d)
}

// POST /api/vdi/desktops/{kind}/{ns}/{member}/start — start a desktop,
// e.g. after a failed start at claim time.
func handleVDIStart(w http.ResponseWriter, r *http.Request, b *vdi.Broker, a vdi.Actor) {
	d, err := b.Start(a, memberRef(r))
	if err != nil {
		errResp(w, vdiStatus(err), err)
		return
	}
	jsonResp(w, http.StatusOK, d)
}

// POST /api/vdi/desktops/{kind}/{ns}/{member}/release — end the claim.
// Authorization is synchronous; the release itself runs as a task (an
// ephemeral member is recreated, which takes minutes). 202 {"task": id}.
func handleVDIRelease(w http.ResponseWriter, r *http.Request, b *vdi.Broker, a vdi.Actor) {
	ref := memberRef(r)
	release, err := b.BeginRelease(a, ref)
	if err != nil {
		errResp(w, vdiStatus(err), err)
		return
	}
	id := fmt.Sprintf("vdi-release-%s-%s-%d", ref.Namespace, ref.Name, time.Now().UnixNano())
	task := tasks.start(id)
	go func() { task.finish(release()) }()
	jsonResp(w, http.StatusAccepted, map[string]string{"task": id, "desktop": ref.ID()})
}
