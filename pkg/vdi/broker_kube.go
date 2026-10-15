package vdi

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

// KubeCluster is the production Cluster. It drives the VM pool primitives
// (Claim, Release) and the CT pool primitives (ClaimCTMember,
// ReleaseCTMember) through kubectl, so the broker inherits their Lease
// compare-and-swap guarantees unchanged.
func KubeCluster() Cluster { return kubeBrokerCluster{} }

type kubeBrokerCluster struct{}

// listClaimLeases lists every VM and CT member Lease in namespace. Both
// kinds carry the member label.
func listClaimLeases(namespace string) ([]lease, error) {
	out, err := run("kubectl", "get", "lease", "-n", namespace, "-l", labelVDIMember, "-o", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []lease `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	return res.Items, nil
}

// claimOf converts a member Lease to a MemberClaim. A Lease with a CT pool label
// belongs to a CT pool member; any other belongs to a VM pool member.
func claimOf(namespace string, l lease) MemberClaim {
	c := MemberClaim{
		Member:   MemberRef{Kind: PoolKindVM, Namespace: namespace, Name: l.Metadata.Labels[labelVDIMember]},
		Pool:     l.Metadata.Labels[labelPool],
		Holder:   l.Spec.HolderIdentity,
		Duration: time.Duration(l.Spec.LeaseDurationSeconds) * time.Second,
		Version:  l.Metadata.ResourceVersion,
	}
	if pool := l.Metadata.Labels[labelCTPool]; pool != "" {
		c.Member.Kind, c.Pool = PoolKindCT, pool
	}
	c.AcquiredAt, _ = time.Parse(time.RFC3339Nano, l.Spec.AcquireTime)
	c.RenewedAt, _ = time.Parse(time.RFC3339Nano, l.Spec.RenewTime)
	return c
}

func (kubeBrokerCluster) Pools(namespace string) ([]PoolStatus, error) {
	vms, err := listPoolVMs(namespace, "")
	if err != nil {
		return nil, fmt.Errorf("list VM pool members: %w", err)
	}
	leases, err := listClaimLeases(namespace)
	if err != nil {
		return nil, fmt.Errorf("list claims: %w", err)
	}
	now := time.Now().UTC()
	live := map[string]bool{}
	for _, l := range leases {
		if l.Metadata.Labels[labelCTPool] == "" && !leaseExpired(l, now) {
			live[l.Metadata.Labels[labelVDIMember]] = true
		}
	}
	byPool := map[string]*PoolStatus{}
	for _, v := range vms {
		name := v.Metadata.Labels[labelPool]
		p, ok := byPool[name]
		if !ok {
			ref := PoolRef{Kind: PoolKindVM, Namespace: namespace, Name: name}
			p = &PoolStatus{ID: ref.ID(), PoolRef: ref, Mode: PoolModePersistent}
			byPool[name] = p
		}
		p.Members++
		switch {
		case live[v.Metadata.Name]:
			p.Claimed++
		case assignedIdentity(v) == "":
			p.Free++
		}
	}
	out := make([]PoolStatus, 0, len(byPool))
	for _, p := range byPool {
		out = append(out, *p)
	}

	specs, _, err := ListCTPools(namespace)
	if err != nil {
		return nil, fmt.Errorf("list CT pools: %w", err)
	}
	for _, spec := range specs {
		members, claims, err := observeCTPool(spec)
		if err != nil {
			return nil, err
		}
		s := ctPoolStatus(spec, members, claims, ctNow())
		ref := PoolRef{Kind: PoolKindCT, Namespace: namespace, Name: spec.Name}
		out = append(out, PoolStatus{ID: ref.ID(), PoolRef: ref, Mode: spec.Mode, Members: s.Desired, Claimed: s.Claimed, Free: s.Free})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (kubeBrokerCluster) Claims(namespace string) ([]MemberClaim, error) {
	leases, err := listClaimLeases(namespace)
	if err != nil {
		return nil, err
	}
	var out []MemberClaim
	for _, l := range leases {
		if c := claimOf(namespace, l); c.Member.Name != "" && c.Holder != "" && c.Holder != CTReconcilerHolder {
			out = append(out, c)
		}
	}
	return out, nil
}

func claimLeaseName(ref MemberRef) string {
	if ref.Kind == PoolKindCT {
		return ctLeasePrefix + ref.Name
	}
	return leaseName(ref.Name)
}

func (kubeBrokerCluster) GetClaim(ref MemberRef) (MemberClaim, bool, error) {
	l, found, err := findLease(ref.Namespace, claimLeaseName(ref))
	if err != nil || !found {
		return MemberClaim{}, false, err
	}
	c := claimOf(ref.Namespace, l)
	if c.Member.Kind != ref.Kind || c.Member.Name != ref.Name {
		// A Lease of the other kind (or a foreign one) under the same name.
		return MemberClaim{}, false, nil
	}
	return c, true, nil
}

func (kubeBrokerCluster) ClaimMember(pool PoolRef, identity string) (string, error) {
	if pool.Kind == PoolKindCT {
		return ClaimCTMember(pool.Namespace, pool.Name, identity)
	}
	return Claim(pool.Namespace, pool.Name, identity)
}

func (kubeBrokerCluster) MemberState(c MemberClaim) (MemberState, error) {
	if c.Member.Kind == PoolKindCT {
		members, err := ctc.ListMembers(c.Member.Namespace, c.Pool)
		if err != nil {
			return MemberState{}, err
		}
		for _, m := range members {
			if m.Name == c.Member.Name {
				state := m.state()
				return MemberState{Phase: string(state), Running: state == CTMemberReady}, nil
			}
		}
		return MemberState{Phase: string(CTMemberMissing)}, nil
	}
	out, err := run("kubectl", "get", "vm", c.Member.Name, "-n", c.Member.Namespace, "-o", "json")
	if err != nil {
		return MemberState{}, err
	}
	var v vmListItem
	if err := json.Unmarshal(out, &v); err != nil {
		return MemberState{}, fmt.Errorf("decode VM %s: %w", c.Member.Name, err)
	}
	phase := v.Status.PrintableStatus
	if phase == "" {
		phase = "Unknown"
	}
	return MemberState{Phase: phase, Running: phase == "Running"}, nil
}

func (kubeBrokerCluster) StartMember(c MemberClaim) error {
	if c.Member.Kind == PoolKindCT {
		return ctc.StartMember(c.Member.Namespace, c.Member.Name)
	}
	return kubevirt.NewClient(c.Member.Namespace).StartVM(c.Member.Name)
}

func (kubeBrokerCluster) ReleaseMember(c MemberClaim) error {
	if c.Member.Kind == PoolKindCT {
		return ReleaseCTMember(c.Member.Namespace, c.Pool, c.Member.Name, c.Holder, false)
	}
	return Release(c.Member.Namespace, c.Member.Name, c.Holder)
}
