package web

import "testing"

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
	old := sampleAllCPU
	sampleAllCPU = func() map[string]int { return map[string]int{"ns/a": 120, "ns/b": 5} }
	defer func() { sampleAllCPU = old }()

	r.sample()
	if len(r.get("ns/a")) != 1 || r.get("ns/a")[0].CPU != 120 {
		t.Errorf("sample did not record ns/a: %+v", r.get("ns/a"))
	}
	if r.get("ns/b")[0].CPU != 5 {
		t.Errorf("sample did not record ns/b: %+v", r.get("ns/b"))
	}
}

func TestCPURing_SampleNilDegrades(t *testing.T) {
	r := &cpuRing{data: map[string][]cpuSample{}, maxSamples: 10}
	old := sampleAllCPU
	sampleAllCPU = func() map[string]int { return nil } // metrics-server absent
	defer func() { sampleAllCPU = old }()

	r.sample() // must not panic
	if len(r.get("ns/a")) != 0 {
		t.Error("expected no samples when source returns nil")
	}
}
