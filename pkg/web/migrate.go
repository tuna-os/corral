package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

// handleMigrate triggers a live migration and tracks it as a background task
// with live progress, polling the VMI's migrationState until it completes or
// fails. The trigger itself runs synchronously so "not migratable" errors
// surface immediately as a 4xx/5xx; the watch then streams progress.
func handleMigrate(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	var b struct {
		TargetNode string `json:"targetNode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b) // empty body = let the scheduler choose

	c := kubeClient(r, ns)
	if err := c.Migrate(name, b.TargetNode); err != nil {
		errResp(w, http.StatusInternalServerError, err)
		return
	}

	id := fmt.Sprintf("migrate-%s-%d", name, time.Now().UnixNano())
	task := tasks.start(id)
	done := taskBegin(requestUser(r), "migrate", ns+"/"+name)
	if b.TargetNode != "" {
		_, _ = fmt.Fprintf(task, "Live migration of %s requested → %s\n", name, b.TargetNode)
	} else {
		_, _ = fmt.Fprintf(task, "Live migration of %s requested (scheduler picks target)\n", name)
	}

	go func() {
		err := watchMigration(c, name, task)
		task.finish(err)
		done(err)
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{"task": id})
}

// Timings for watchMigration — package vars so tests can shrink them.
var (
	migrationTimeout      = 10 * time.Minute
	migrationSettle       = 2 * time.Second // wait for the VMIM to register
	migrationPollInterval = 2 * time.Second
)

// watchMigration polls a VM's migrationState, writing a line on each phase
// change, until it completes (nil), fails, or times out.
func watchMigration(c kubevirt.VMAdvanced, name string, w io.Writer) error {
	time.Sleep(migrationSettle) // avoid reading a stale state from a prior migration
	deadline := time.Now().Add(migrationTimeout)
	last := ""
	for time.Now().Before(deadline) {
		st, err := c.MigrationState(name)
		if err == nil && st.Present {
			phase := "pending"
			switch {
			case st.Completed:
				phase = "completed"
			case st.Failed:
				phase = "failed"
			case st.Active:
				phase = "migrating"
			}
			if phase != last {
				_, _ = fmt.Fprintf(w, "%s → %s: %s\n", or(st.SourceNode, "?"), or(st.TargetNode, "?"), phase)
				last = phase
			}
			if st.Completed {
				_, _ = fmt.Fprintf(w, "Migration complete — now running on %s\n", st.TargetNode)
				return nil
			}
			if st.Failed {
				return fmt.Errorf("live migration of %s failed", name)
			}
		}
		time.Sleep(migrationPollInterval)
	}
	return fmt.Errorf("live migration of %s timed out after %s", name, migrationTimeout)
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
