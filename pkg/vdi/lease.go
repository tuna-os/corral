package vdi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The claim primitive (#226): one coordination.k8s.io Lease per pool
// member. Creating the Lease is first-writer-wins; every later transition
// (takeover of an expired claim, release) is preconditioned on the Lease's
// resourceVersion, so two writers can never both succeed.

const labelVDIMember = "corral.dev/vdi-member"

type lease struct {
	Metadata struct {
		Name            string            `json:"name"`
		ResourceVersion string            `json:"resourceVersion"`
		Labels          map[string]string `json:"labels,omitempty"`
	} `json:"metadata"`
	Spec struct {
		HolderIdentity       string `json:"holderIdentity"`
		AcquireTime          string `json:"acquireTime"`
		RenewTime            string `json:"renewTime"`
		LeaseDurationSeconds int32  `json:"leaseDurationSeconds"`
	} `json:"spec"`
}

// leaseRef names one member's Lease and the labels it carries.
type leaseRef struct {
	Namespace string
	Name      string
	Labels    map[string]string
}

func leaseName(member string) string { return leasePrefix + member }

// vmLeaseRef is the Lease of a VM-backed pool member.
func vmLeaseRef(namespace, pool, member string) leaseRef {
	return leaseRef{Namespace: namespace, Name: leaseName(member), Labels: map[string]string{labelPool: pool, labelVDIMember: member}}
}

func leaseExpired(l lease, now time.Time) bool {
	if l.Spec.HolderIdentity == "" || l.Spec.RenewTime == "" || l.Spec.LeaseDurationSeconds <= 0 {
		return true
	}
	renewed, err := time.Parse(time.RFC3339Nano, l.Spec.RenewTime)
	return err != nil || now.After(renewed.Add(time.Duration(l.Spec.LeaseDurationSeconds)*time.Second))
}

func leaseManifest(ref leaseRef, holder string, duration time.Duration, now time.Time, resourceVersion string) []byte {
	metadata := map[string]any{"name": ref.Name, "namespace": ref.Namespace, "labels": ref.Labels}
	if resourceVersion != "" {
		metadata["resourceVersion"] = resourceVersion
	}
	data, err := json.Marshal(map[string]any{
		"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": metadata,
		"spec": map[string]any{
			"holderIdentity": holder, "acquireTime": now.Format(time.RFC3339Nano),
			"renewTime": now.Format(time.RFC3339Nano), "leaseDurationSeconds": int32(duration.Seconds()),
		},
	})
	if err != nil {
		panic(err)
	}
	return data
}

// createLease attempts the first-writer-wins creation. When the Lease
// already exists it returns created=false with the current Lease so the
// caller can decide whether a takeover is allowed.
func createLease(ref leaseRef, holder string, duration time.Duration, now time.Time) (bool, lease, error) {
	_, err := runner.RunStdin(string(leaseManifest(ref, holder, duration, now, "")), "kubectl", "create", "-f", "-")
	if err == nil {
		return true, lease{}, nil
	}
	current, getErr := readLease(ref.Namespace, ref.Name)
	if getErr != nil {
		return false, lease{}, fmt.Errorf("claim %s: lease create failed (%v); read failed: %w", ref.Name, err, getErr)
	}
	return false, current, nil
}

// replaceLease is a compare-and-swap on resourceVersion. Any failure is
// reported as a lost race (false), never as success.
func replaceLease(ref leaseRef, holder string, duration time.Duration, now time.Time, resourceVersion string) bool {
	_, err := runner.RunStdin(string(leaseManifest(ref, holder, duration, now, resourceVersion)), "kubectl", "replace", "-f", "-")
	return err == nil
}

// deleteLease removes a Lease only if it still has resourceVersion.
// `kubectl delete` has no precondition flag, so this sends DeleteOptions
// with a resourceVersion precondition through the raw API. A Lease that is
// already gone counts as deleted.
func deleteLease(namespace, name, resourceVersion string) error {
	body, err := json.Marshal(map[string]any{
		"kind": "DeleteOptions", "apiVersion": "v1",
		"preconditions": map[string]string{"resourceVersion": resourceVersion},
	})
	if err != nil {
		return err
	}
	path := "/apis/coordination.k8s.io/v1/namespaces/" + namespace + "/leases/" + name
	out, err := runner.RunStdin(string(body), "kubectl", "delete", "--raw", path, "-f", "-")
	if err != nil && !isNotFound(out, err) {
		return fmt.Errorf("delete lease %s: %w", name, err)
	}
	return nil
}

func readLease(namespace, name string) (lease, error) {
	out, err := run("kubectl", "get", "lease", name, "-n", namespace, "-o", "json")
	if err != nil {
		return lease{}, err
	}
	var l lease
	if err := json.Unmarshal(out, &l); err != nil {
		return lease{}, fmt.Errorf("decode claim lease: %w", err)
	}
	return l, nil
}

// findLease is readLease that reports an absent Lease as found=false
// instead of an error.
func findLease(namespace, name string) (lease, bool, error) {
	out, err := run("kubectl", "get", "lease", name, "-n", namespace, "--ignore-not-found", "-o", "json")
	if err != nil {
		return lease{}, false, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return lease{}, false, nil
	}
	var l lease
	if err := json.Unmarshal(out, &l); err != nil {
		return lease{}, false, fmt.Errorf("decode claim lease: %w", err)
	}
	return l, true, nil
}

func isNotFound(out []byte, err error) bool {
	msg := strings.ToLower(string(out))
	if err != nil {
		msg += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(msg, "notfound") || strings.Contains(msg, "not found")
}

// acquireLease uses Lease creation as a first-writer-wins primitive. An
// expired lease is replaced with its resourceVersion, making recovery a
// compare-and-swap instead of another list-then-label race.
func acquireLease(namespace, pool, member, identity string) (bool, error) {
	now := time.Now().UTC()
	ref := vmLeaseRef(namespace, pool, member)
	created, current, err := createLease(ref, identity, claimDuration, now)
	if err != nil || created {
		return created, err
	}
	if current.Spec.HolderIdentity == identity && !leaseExpired(current, now) {
		return true, nil
	}
	if !leaseExpired(current, now) {
		return false, nil
	}
	return replaceLease(ref, identity, claimDuration, now, current.Metadata.ResourceVersion), nil
}
