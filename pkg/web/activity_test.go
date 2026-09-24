package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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
