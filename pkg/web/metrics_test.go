package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

func TestCPURing_AddTrim(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 3}
	for i := 1; i <= 5; i++ {
		r.add("ns/vm", cpuSample{T: int64(i), CPU: i * 10})
	}
	got := r.get("ns/vm")
	if len(got) != 3 {
		t.Fatalf("expected ring trimmed to 3, got %d", len(got))
	}
	// Oldest dropped: should retain samples 3,4,5.
	if got[0].CPU != 30 || got[2].CPU != 50 {
		t.Errorf("unexpected retained window: %+v", got)
	}
}

func TestCPURing_GetEmpty(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 3}
	if got := r.get("missing"); len(got) != 0 {
		t.Errorf("expected empty slice for unknown key, got %v", got)
	}
}

func TestCPURing_GetCopyIsolated(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 3}
	r.add("ns/vm", cpuSample{T: 1, CPU: 10})
	got := r.get("ns/vm")
	got[0].CPU = 999 // mutating the copy must not corrupt the ring
	if r.get("ns/vm")[0].CPU != 10 {
		t.Error("get() must return an isolated copy")
	}
}

func TestCPURing_Sample(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}
	r.sampleUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{
			"ns/a": {MilliCPU: 120, MemBytes: 1 << 30, Node: "n1"},
			"ns/b": {MilliCPU: 5, MemBytes: 1 << 20, Node: "n1"},
			"ns/c": {MilliCPU: 40, Node: "n2"},
		}
	}

	r.sample()
	if got := r.get("ns/a"); len(got) != 1 || got[0].CPU != 120 || got[0].Mem != 1<<30 {
		t.Errorf("sample did not record ns/a: %+v", got)
	}
	if r.get("ns/b")[0].CPU != 5 {
		t.Errorf("sample did not record ns/b: %+v", r.get("ns/b"))
	}
	// The roll-ups are what the node and datacenter charts plot.
	if n1 := r.get(nodeKey("n1")); len(n1) != 1 || n1[0].CPU != 125 || n1[0].Mem != 1<<30+1<<20 {
		t.Errorf("node n1 roll-up = %+v, want 125m and a+b memory", n1)
	}
	if dc := r.get(dcKey); len(dc) != 1 || dc[0].CPU != 165 {
		t.Errorf("datacenter roll-up = %+v, want 165m", dc)
	}
}

func TestCPURing_SampleNilDegrades(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}
	r.sampleUsage = func() map[string]kubevirt.Usage { return nil } // metrics-server absent

	r.sample() // must not panic
	if len(r.get("ns/a")) != 0 || len(r.get(dcKey)) != 0 {
		t.Error("expected no samples when source returns nil")
	}
}

func TestCPURing_CurrentDropsStoppedVMs(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}

	r.sampleUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{"ns/a": {MilliCPU: 10}, "ns/b": {MilliCPU: 20}}
	}
	r.sample()
	time.Sleep(2 * time.Millisecond) // the next tick needs a later timestamp
	r.sampleUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{"ns/b": {MilliCPU: 30, Node: "n1"}}
	}
	r.sample()

	cur := r.current()
	if len(cur) != 1 || cur[0].Name != "b" || cur[0].CPU != 30 || cur[0].Node != "n1" {
		t.Errorf("current() = %+v, want only ns/b at its newest reading", cur)
	}
}

func TestTopVMs(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}
	r.sampleUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{
			"ns/small": {MilliCPU: 10, MemBytes: 8 << 30, Node: "n1"},
			"ns/busy":  {MilliCPU: 900, MemBytes: 1 << 30, Node: "n1"},
			"ns/other": {MilliCPU: 500, MemBytes: 2 << 30, Node: "n2"},
		}
	}
	r.sample()

	get := func(query string) []vmUsage {
		t.Helper()
		rec := httptest.NewRecorder()
		r.handleTopVMs(rec, httptest.NewRequest("GET", "/api/metrics/top?"+query, nil))
		var rows []vmUsage
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return rows
	}
	names := func(rows []vmUsage) (out []string) {
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}
	if got := names(get("")); !reflect.DeepEqual(got, []string{"busy", "other", "small"}) {
		t.Errorf("default order = %v, want busiest CPU first", got)
	}
	if got := names(get("by=mem&limit=2")); !reflect.DeepEqual(got, []string{"small", "other"}) {
		t.Errorf("by=mem&limit=2 = %v, want the two largest by memory", got)
	}
	if got := names(get("node=n1")); !reflect.DeepEqual(got, []string{"busy", "small"}) {
		t.Errorf("node=n1 = %v, want only that node's VMs", got)
	}
}

// Independent routers must never read each other's histories or source seams.
func TestMetricsRouterIsolation(t *testing.T) {
	routers := make([]http.Handler, 2)
	for i := range routers {
		cpu := (i + 1) * 100
		hist := newCPURing(func() map[string]kubevirt.Usage {
			return map[string]kubevirt.Usage{"ns/vm": {MilliCPU: cpu, Node: "node"}}
		})
		hist.sample()
		mux, err := routesWithMetrics(false, hist)
		if err != nil {
			t.Fatal(err)
		}
		routers[i] = mux
	}
	for i, router := range routers {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			for _, path := range []string{"/api/metrics/history", "/api/nodes/node/metrics/history", "/api/vms/ns/vm/metrics/history"} {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
				var samples []cpuSample
				if err := json.Unmarshal(rec.Body.Bytes(), &samples); err != nil {
					t.Fatal(err)
				}
				if rec.Code != 200 || len(samples) != 1 || samples[0].CPU != (i+1)*100 {
					t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
				}
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/metrics/top", nil))
			var rows []vmUsage
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].CPU != (i+1)*100 {
				t.Fatalf("top: %+v", rows)
			}
		})
	}
}

func TestMetricSamplerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hist := newCPURing(func() map[string]kubevirt.Usage {
		cancel() // cancellation during the initial collection must also stop the loop
		return map[string]kubevirt.Usage{"ns/vm": {MilliCPU: 12}}
	})
	done := make(chan struct{})
	go func() { defer close(done); hist.runSampler(ctx) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sampler did not stop")
	}
	if got := hist.get("ns/vm"); len(got) != 1 || got[0].CPU != 12 {
		t.Fatalf("seed: %+v", got)
	}
	hist.sampleUsage = func() map[string]kubevirt.Usage { t.Fatal("sampled after cancellation"); return nil }
	hist.runSampler(ctx)
}

func TestMetricsHistoryEmpty(t *testing.T) {
	hist := newCPURing(func() map[string]kubevirt.Usage { return nil })
	mux, err := routesWithMetrics(false, hist)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/metrics/history", "/api/nodes/unknown/metrics/history", "/api/vms/ns/unknown/metrics/history", "/api/metrics/top"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
