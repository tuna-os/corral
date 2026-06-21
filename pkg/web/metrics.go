package web

import (
	"sync"
	"time"

	"github.com/hanthor/corral/pkg/kubevirt"
)

// cpuSample is one CPU reading for a VM: epoch-millis timestamp + millicores.
type cpuSample struct {
	T   int64 `json:"t"`   // unix epoch milliseconds
	CPU int   `json:"cpu"` // millicores (1000 = one full vCPU)
}

// cpuRing is a bounded in-memory history of per-VM CPU samples. It's the first
// slice of a Proxmox-style RRD story — CPU only, no external TSDB. Samples are
// dropped oldest-first past maxSamples; a VM that disappears ages out naturally.
type cpuRing struct {
	mu         sync.Mutex
	data       map[string][]cpuSample // "namespace/vm" -> samples (oldest first)
	maxSamples int
}

// cpuHist retains ~1h at the 15s sample interval (240 samples per VM).
var cpuHist = &cpuRing{data: map[string][]cpuSample{}, maxSamples: 240}

const metricSampleInterval = 15 * time.Second

func (r *cpuRing) add(key string, s cpuSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	buf := append(r.data[key], s)
	if len(buf) > r.maxSamples {
		buf = buf[len(buf)-r.maxSamples:]
	}
	r.data[key] = buf
}

func (r *cpuRing) get(key string) []cpuSample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]cpuSample, len(r.data[key]))
	copy(out, r.data[key])
	return out
}

// sampleCPU joins one cluster-wide reading into the ring. Exposed as a seam so
// tests can drive it without a ticker.
var sampleAllCPU = kubevirt.SampleAllCPU

func (r *cpuRing) sample() {
	now := time.Now().UnixMilli()
	for key, milli := range sampleAllCPU() {
		r.add(key, cpuSample{T: now, CPU: milli})
	}
}

// startMetricSampler kicks off the background CPU sampler. It samples on a fixed
// interval regardless of whether metrics-server is up yet — SampleAllCPU returns
// nil when it's absent, so ticks are cheap no-ops until it comes online.
func startMetricSampler() {
	go func() {
		cpuHist.sample() // seed immediately so the first graph isn't empty
		t := time.NewTicker(metricSampleInterval)
		defer t.Stop()
		for range t.C {
			cpuHist.sample()
		}
	}()
}
