package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	forceDelete   bool
	deleteWait    bool
	deleteTimeout time.Duration
)

var deleteCmd = &cobra.Command{
	Use:   "delete [name...]",
	Short: "Delete VMs and everything created for them",
	Long: `Delete one or more VMs with their disks and every other object corral
created for them. On KubeVirt that includes Secrets, proxies, snapshots and
the builder VM and disk that an interrupted ` + "`corral bootc create`" + ` leaves
behind, so a name that only has such leftovers can be deleted too.

--wait blocks until the deleted objects are gone, and fails if any object
remains after --timeout. --json prints one result per VM:
{"name", "deleted", "removed": ["kind/name", ...], "error"}.`,
	Example: `  corral delete myvm                     # asks to confirm
  corral delete myvm -f                  # skip the confirmation
  corral delete vm1 vm2 -f --wait        # block until every object is gone
  corral delete vm1 vm2 -f --wait --json # one result per VM, for scripts`,
	RunE: runDelete,
}

func init() {
	rootCmd.AddCommand(deleteCmd)
	deleteCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation")
	deleteCmd.Flags().BoolVar(&deleteWait, "wait", false, "Block until the VM and its objects are gone")
	deleteCmd.Flags().DurationVar(&deleteTimeout, "timeout", 5*time.Minute, "How long --wait waits")
}

// deleteResult is one VM's outcome, as `corral delete --json` prints it.
// Removed names each object the backend removed, as "kind/name"; backends
// that delete a VM as one unit report none.
type deleteResult struct {
	Name    string   `json:"name"`
	Deleted bool     `json:"deleted"`
	Removed []string `json:"removed"`
	Error   string   `json:"error,omitempty"`
	err     error
}

// deleteTarget is one name resolved to whatever deletes it.
type deleteTarget struct {
	name    string
	ref     types.InstanceRef
	peer    *peerTarget
	adapter backend.Adapter
	// notListed is the lookup error for a name no backend lists; the default
	// context's Purger then looks for what an interrupted create left behind.
	notListed error
	err       error
}

func runDelete(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		name, err := requireOrPrompt(nil, "delete")
		if err != nil {
			return err
		}
		args = []string{name}
	}
	targets := make([]deleteTarget, len(args))
	var confirm []string
	for i, name := range args {
		targets[i] = resolveDeleteTarget(name)
		if targets[i].err == nil {
			confirm = append(confirm, name)
		}
	}
	// One name keeps the old contract: a name that resolves to nothing
	// fails before any prompt.
	if len(targets) == 1 && targets[0].err != nil {
		return targets[0].err
	}
	if !forceDelete && len(confirm) > 0 && !confirmDelete(confirm) {
		return fmt.Errorf("aborted")
	}

	results := make([]deleteResult, len(targets))
	for i := range targets {
		results[i] = targets[i].delete()
	}
	if deleteWait {
		deadline := time.Now().Add(deleteTimeout)
		for i := range targets {
			targets[i].wait(&results[i], time.Until(deadline))
		}
	}
	return reportDeletes(results)
}

// resolveDeleteTarget finds what deletes name. A name that no backend lists
// still resolves when the default context can purge by name, because an
// interrupted create leaves objects under a name that has no VM yet.
func resolveDeleteTarget(name string) deleteTarget {
	t := deleteTarget{name: name, ref: types.InstanceRef{Name: name}}
	resolved, err := requireOrPrompt([]string{name}, "delete")
	if err != nil {
		var notFound fleet.NotFoundError
		if errors.As(err, &notFound) {
			if adapter, ok := leftoverAdapter(name); ok {
				t.adapter, t.notListed = adapter, err
				return t
			}
		}
		t.err = err
		return t
	}
	p, peer, err := resolvePeerTarget(resolved)
	if err != nil {
		t.err = err
		return t
	}
	if peer {
		t.peer = &p
		return t
	}
	t.ref, t.err = instanceRef(resolved)
	if t.err != nil {
		return t
	}
	t.adapter, t.err = backend.For(t.ref)
	if t.err == nil {
		if _, ok := t.adapter.(backend.Power); !ok {
			t.err = unsupported(t.adapter, "delete")
		}
	}
	return t
}

// leftoverAdapter is the default context's adapter, when it can purge by
// name: the --context flag's context, else the configured default.
func leftoverAdapter(name string) (backend.Adapter, bool) {
	target := config.DefaultContext()
	if rootContext != "" {
		found, ok := config.FindContext(rootContext)
		if !ok {
			return nil, false
		}
		target = found
	}
	adapter, err := backend.For(types.InstanceRef{Backend: target.Backend, Context: target.Context, Name: name})
	if err != nil {
		return nil, false
	}
	_, ok := adapter.(backend.Purger)
	return adapter, ok
}

func confirmDelete(names []string) bool {
	if len(names) == 1 {
		fmt.Fprintf(os.Stderr, "Delete VM %q and its disks? [y/N] ", names[0])
	} else {
		fmt.Fprintf(os.Stderr, "Delete %d VMs (%s) and their disks? [y/N] ", len(names), strings.Join(names, ", "))
	}
	var resp string
	_, _ = fmt.Fscanln(os.Stdin, &resp) // no answer is a no
	return resp == "y" || resp == "Y" || resp == "yes"
}

// delete removes the target and reports what went.
func (t deleteTarget) delete() deleteResult {
	result := deleteResult{Name: t.name, Removed: []string{}}
	err := t.err
	switch {
	case err != nil:
	case t.peer != nil:
		err = t.peer.action("DELETE", "")
	default:
		if purger, ok := t.adapter.(backend.Purger); ok {
			var removed []string
			removed, err = purger.Purge(t.ref.Name)
			result.Removed = append(result.Removed, removed...)
		} else {
			err = t.adapter.(backend.Power).Delete(t.ref.Name)
		}
		if err == nil && t.notListed != nil && len(result.Removed) == 0 {
			err = t.notListed // nothing listed and nothing left behind
		}
	}
	if err != nil {
		result.Error, result.err = err.Error(), err
		return result
	}
	result.Deleted = true
	if registryStore != nil && t.peer == nil {
		if entry, ok := registryStore.Get(t.ref.Name); ok && entry.Peer == t.ref.Peer &&
			backend.Ref(entry.Backend, entry.Context, entry.Namespace, t.ref.Name) ==
				backend.Ref(t.ref.Backend, t.ref.Context, t.ref.Namespace, t.ref.Name) {
			if err := registryStore.Remove(t.ref.Name); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s is deleted, but its registry entry stays: %v\n", t.name, err)
			}
		}
	}
	return result
}

// wait blocks until what the target removed is gone. Only a Purger reports
// objects to wait on; other backends delete a VM before they return.
func (t deleteTarget) wait(result *deleteResult, timeout time.Duration) {
	purger, ok := t.adapter.(backend.Purger)
	if !result.Deleted || !ok {
		return
	}
	if err := purger.WaitRemoved(result.Removed, timeout); err != nil {
		result.Deleted = false
		result.Error, result.err = err.Error(), err
	}
}

// reportDeletes prints the results and returns an error when any VM was
// not deleted.
func reportDeletes(results []deleteResult) error {
	var failed []deleteResult
	for _, r := range results {
		if !r.Deleted {
			failed = append(failed, r)
		}
	}
	if jsonOutput {
		if err := printJSON(results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			if !r.Deleted {
				continue
			}
			if len(r.Removed) > 0 {
				fmt.Printf("Deleted %s (%d objects)\n", r.Name, len(r.Removed))
			} else {
				fmt.Printf("Deleted %s\n", r.Name)
			}
		}
	}
	switch {
	case len(failed) == 0:
		return nil
	case len(results) == 1:
		return failed[0].err
	}
	if !jsonOutput {
		for _, r := range failed {
			fmt.Fprintf(os.Stderr, "%s: %s\n", r.Name, r.Error)
		}
	}
	return fmt.Errorf("%d of %d deletes failed", len(failed), len(results))
}
