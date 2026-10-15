package proxmoxbe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/logging"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A client bound to a request logs each API call with the request ID, and
// never the token that authorizes it.
func TestBoundClientLogsAPICallsWithRequestID(t *testing.T) {
	var out syncBuffer
	h, err := logging.NewHandler(&out, slog.LevelDebug, "json")
	if err != nil {
		t.Fatal(err)
	}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })

	f := newFakePVE(t)
	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "req-pve"))
	cancel() // values travel, cancellation does not
	if _, err := f.client().WithContext(ctx).List(); err != nil {
		t.Fatalf("List through a bound client: %v", err)
	}

	logs := out.String()
	if strings.Contains(logs, "00000000-0000-0000-0000-000000000000") || strings.Contains(logs, "PVEAPIToken") {
		t.Fatalf("API-call log leaks the token:\n%s", logs)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		if rec["msg"] != "proxmox api" {
			continue
		}
		found = true
		if rec[logging.RequestIDKey] != "req-pve" || rec["method"] != "GET" || rec["status"] != float64(200) {
			t.Errorf("record = %v, want request_id=req-pve, method GET, status 200", rec)
		}
	}
	if !found {
		t.Fatalf("no proxmox api records:\n%s", logs)
	}
}
