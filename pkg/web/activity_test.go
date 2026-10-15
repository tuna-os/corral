package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/config"
)

func TestActivityTrackerIgnoresMachinery(t *testing.T) {
	h := activityTracker(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	old := time.Now().Add(-5 * time.Hour).UnixNano()

	for _, tc := range []struct {
		path, ua string
		counts   bool
	}{
		{"/healthz", "", false},
		{"/readyz", "", false},
		{"/metrics", "Prometheus/2", false},
		{"/", "kube-probe/1.36", false},
		{"/api/v1/inventory", collectorUserAgent, false},
		{"/api/v1/inventory", "Go-http-client/1.1", true},
		{"/api/vms", "Mozilla/5.0", true},
		{"/", "Mozilla/5.0", true},
	} {
		lastActivity.Store(old)
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("User-Agent", tc.ua)
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got := lastActivity.Load() != old; got != tc.counts {
			t.Errorf("%s (UA %q): counted=%v, want %v", tc.path, tc.ua, got, tc.counts)
		}
	}
}

func TestActivityMetricIsExposed(t *testing.T) {
	lastActivity.Store(time.Unix(1790000000, 0).UnixNano())
	rec := httptest.NewRecorder()
	handleMetricsExposition(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE corral_last_activity_timestamp_seconds gauge") {
		t.Fatalf("activity gauge not declared:\n%s", body)
	}
	// Parse the value rather than match its text: the exposition may render
	// 1790000000 or 1.79e+09 depending on the formatter.
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "corral_last_activity_timestamp_seconds "); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || f < 1789999999 || f > 1790000001 {
				t.Fatalf("activity gauge = %q, want 1790000000", v)
			}
			return
		}
	}
	t.Fatalf("activity gauge has no sample:\n%s", body)
}

// A hub's --metrics collector polls every peer on a timer; those polls must
// reach the peer marked as machinery, or no peer host would ever look idle.
func TestCollectorPeerPollsAreNotActivity(t *testing.T) {
	var counted []bool
	peer := httptest.NewServer(activityTracker(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counted = append(counted, !isMachineryRequest(r))
		_, _ = w.Write([]byte(`{"vms":[]}`))
	})))
	defer peer.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := config.SetPeer("p", peer.URL); err != nil {
		t.Fatal(err)
	}

	fetchPeerVMs(backgroundPoll(context.Background()))
	peerVMs()
	if len(counted) != 2 || counted[0] || !counted[1] {
		t.Fatalf("counted = %v, want [false true] (collector poll, then user-driven fetch)", counted)
	}
}
