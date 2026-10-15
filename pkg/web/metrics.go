package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

// cpuSample is one usage reading: epoch-millis timestamp, millicores and, when
// metrics-server reports it, working-set memory in bytes. The name predates
// the memory field; the JSON shape only grew.
type cpuSample struct {
	T   int64 `json:"t"`             // unix epoch milliseconds
	CPU int   `json:"cpu"`           // millicores (1000 = one full vCPU)
	Mem int64 `json:"mem,omitempty"` // bytes
}

// cpuRing is a bounded in-memory history of usage samples. It's the first
// slice of a Proxmox-style RRD story — no external TSDB. Samples are dropped
// oldest-first past maxSamples; a VM that disappears ages out naturally.
//
// Keys are "namespace/vm" for VMs, plus two roll-ups that the dashboard
// charts: nodeKey(name) sums the VMs on one node and dcKey sums them all.
// Neither can collide with a VM key, because a namespace cannot hold "@".
type cpuRing struct {
	mu         sync.Mutex
	data       map[string][]cpuSample // key -> samples (oldest first)
	nodes      map[string]string      // "namespace/vm" -> node at the last tick
	lastTick   int64                  // T of the newest sample() that got data
	maxSamples int
}

const dcKey = "@dc"

func nodeKey(name string) string { return "@node/" + name }

// cpuHist retains ~1h at the 15s sample interval (240 samples per VM).
var cpuHist = &cpuRing{data: map[string][]cpuSample{}, maxSamples: 240}

const metricSampleInterval = 15 * time.Second

func (r *cpuRing) add(key string, s cpuSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(key, s)
}

func (r *cpuRing) addLocked(key string, s cpuSample) {
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

// sampleAllUsage joins one cluster-wide reading into the ring. Exposed as a
// seam so tests can drive it without a ticker.
var sampleAllUsage = kubevirt.SampleAllUsage

func (r *cpuRing) sample() {
	usage := sampleAllUsage()
	if usage == nil {
		return // metrics-server absent: record nothing rather than zeros
	}
	now := time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		r.nodes = map[string]string{}
	}
	total := cpuSample{T: now}
	perNode := map[string]cpuSample{}
	for key, u := range usage {
		r.addLocked(key, cpuSample{T: now, CPU: u.MilliCPU, Mem: u.MemBytes})
		r.nodes[key] = u.Node
		total.CPU += u.MilliCPU
		total.Mem += u.MemBytes
		if u.Node != "" {
			n := perNode[u.Node]
			n.T, n.CPU, n.Mem = now, n.CPU+u.MilliCPU, n.Mem+u.MemBytes
			perNode[u.Node] = n
		}
	}
	r.addLocked(dcKey, total)
	for node, s := range perNode {
		r.addLocked(nodeKey(node), s)
	}
	r.lastTick = now
}

// vmUsage is one row of GET /api/metrics/top.
type vmUsage struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Node      string `json:"node,omitempty"`
	CPU       int    `json:"cpu"`
	Mem       int64  `json:"mem,omitempty"`
}

// current returns the newest reading of every VM that was reported by the
// most recent tick — a stopped VM keeps its history but drops out of here.
func (r *cpuRing) current() []vmUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []vmUsage{}
	for key, buf := range r.data {
		if strings.HasPrefix(key, "@") || len(buf) == 0 || buf[len(buf)-1].T != r.lastTick {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		last := buf[len(buf)-1]
		out = append(out, vmUsage{Namespace: ns, Name: name, Node: r.nodes[key], CPU: last.CPU, Mem: last.Mem})
	}
	return out
}

// startMetricSampler kicks off the background usage sampler. It samples on a
// fixed interval regardless of whether metrics-server is up yet —
// SampleAllUsage returns nil when it's absent, so ticks are cheap no-ops until
// it comes online.
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

func historyResp(w http.ResponseWriter, key string) {
	samples := cpuHist.get(key)
	if samples == nil {
		samples = []cpuSample{}
	}
	jsonResp(w, http.StatusOK, samples)
}

// GET /api/metrics/history — the datacenter roll-up: CPU and memory summed
// over every running VM, one sample per tick.
func handleDCMetricsHistory(w http.ResponseWriter, _ *http.Request) {
	historyResp(w, dcKey)
}

// GET /api/nodes/{name}/metrics/history — the same roll-up for one node.
func handleNodeMetricsHistory(w http.ResponseWriter, r *http.Request) {
	historyResp(w, nodeKey(r.PathValue("name")))
}

// GET /api/metrics/top?by=cpu|mem&node=NAME&limit=N — the busiest running VMs
// at the last tick, for the dashboard's "top VMs" widgets.
func handleTopVMs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows := cpuHist.current()
	if node := q.Get("node"); node != "" {
		kept := rows[:0]
		for _, v := range rows {
			if v.Node == node {
				kept = append(kept, v)
			}
		}
		rows = kept
	}
	byMem := q.Get("by") == "mem"
	sort.Slice(rows, func(i, j int) bool {
		if byMem && rows[i].Mem != rows[j].Mem {
			return rows[i].Mem > rows[j].Mem
		}
		if !byMem && rows[i].CPU != rows[j].CPU {
			return rows[i].CPU > rows[j].CPU
		}
		return rows[i].Namespace+"/"+rows[i].Name < rows[j].Namespace+"/"+rows[j].Name
	})
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < len(rows) {
		rows = rows[:n]
	}
	jsonResp(w, http.StatusOK, rows)
}
