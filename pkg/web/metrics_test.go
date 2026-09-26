package web

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
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
	old := sampleAllUsage
	sampleAllUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{
			"ns/a": {MilliCPU: 120, MemBytes: 1 << 30, Node: "n1"},
			"ns/b": {MilliCPU: 5, MemBytes: 1 << 20, Node: "n1"},
			"ns/c": {MilliCPU: 40, Node: "n2"},
		}
	}
	defer func() { sampleAllUsage = old }()

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
	old := sampleAllUsage
	sampleAllUsage = func() map[string]kubevirt.Usage { return nil } // metrics-server absent
	defer func() { sampleAllUsage = old }()

	r.sample() // must not panic
	if len(r.get("ns/a")) != 0 || len(r.get(dcKey)) != 0 {
		t.Error("expected no samples when source returns nil")
	}
}

func TestCPURing_CurrentDropsStoppedVMs(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}
	old := sampleAllUsage
	defer func() { sampleAllUsage = old }()

	sampleAllUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{"ns/a": {MilliCPU: 10}, "ns/b": {MilliCPU: 20}}
	}
	r.sample()
	time.Sleep(2 * time.Millisecond) // the next tick needs a later timestamp
	sampleAllUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{"ns/b": {MilliCPU: 30, Node: "n1"}}
	}
	r.sample()

	cur := r.current()
	if len(cur) != 1 || cur[0].Name != "b" || cur[0].CPU != 30 || cur[0].Node != "n1" {
		t.Errorf("current() = %+v, want only ns/b at its newest reading", cur)
	}
}

func TestTopVMs(t *testing.T) {
	old, oldHist := sampleAllUsage, cpuHist
	defer func() { sampleAllUsage, cpuHist = old, oldHist }()
	cpuHist = &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}
	sampleAllUsage = func() map[string]kubevirt.Usage {
		return map[string]kubevirt.Usage{
			"ns/small": {MilliCPU: 10, MemBytes: 8 << 30, Node: "n1"},
			"ns/busy":  {MilliCPU: 900, MemBytes: 1 << 30, Node: "n1"},
			"ns/other": {MilliCPU: 500, MemBytes: 2 << 30, Node: "n2"},
		}
	}
	cpuHist.sample()

	get := func(query string) []vmUsage {
		t.Helper()
		rec := httptest.NewRecorder()
		handleTopVMs(rec, httptest.NewRequest("GET", "/api/metrics/top?"+query, nil))
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
