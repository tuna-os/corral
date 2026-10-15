package web

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tuna-os/corral/pkg/promtext"
)

// Activity tracking: when did a person (or a peer Corral) last use this
// server? Exposed as corral_last_activity_timestamp_seconds so an operator can
// power down an on-demand VM host nobody is using (see host-power), or alert
// on a dashboard nobody has opened in weeks.
//
// Only requests the auth gate accepted count, and machinery never does:
// kubelet probes, /healthz, /readyz and /metrics scrapes would otherwise keep
// the timestamp fresh forever and make "idle" unobservable. An open browser
// tab does count, because the UI polls the API: someone is looking. A peer
// Corral's request counts when a user drove it (a dashboard listing the
// fleet), but not when its --metrics collector polls on a timer: a hub's
// collector would otherwise keep every peer host awake forever.

var lastActivity atomic.Int64 // unix nanoseconds

func init() {
	// A restart is not evidence of idleness: start the clock at boot.
	lastActivity.Store(time.Now().UnixNano())
}

func isMachineryRequest(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	ua := r.UserAgent()
	return strings.HasPrefix(ua, "kube-probe/") || strings.HasPrefix(ua, collectorUserAgent)
}

// collectorUserAgent identifies peer requests made by the background metrics
// collector, so the peer does not count them as activity.
const collectorUserAgent = "corral-collector"

type backgroundPollKey struct{}

// backgroundPoll marks ctx as belonging to timer-driven collection rather
// than a user request.
func backgroundPoll(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundPollKey{}, true)
}

func isBackgroundPoll(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundPollKey{}).(bool)
	return v
}

func activityTracker(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMachineryRequest(r) {
			lastActivity.Store(time.Now().UnixNano())
		}
		next.ServeHTTP(w, r)
	})
}

// activityMetrics renders the activity gauge; appended to /metrics whether
// or not fleet collection is running.
func activityMetrics() string {
	out := promtext.New()
	out.Metric("corral_last_activity_timestamp_seconds", "gauge",
		"Unix time of the last request from a user or peer (probes and metric scrapes excluded); process start if none yet")
	out.Sample(nil, float64(lastActivity.Load())/1e9)
	return out.String()
}
