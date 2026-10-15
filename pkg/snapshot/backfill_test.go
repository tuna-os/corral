package snapshot

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/proxmoxbe"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

// stubKubevirt stands in for *kubevirt.Client behind the kubevirtClientFor
// seam: no cluster, just canned answers.
type stubKubevirt struct {
	snapName string
	snapErr  error
	infos    []kubevirt.SnapshotInfo
	listErr  error

	restored   string
	restoreErr error
	deleted    string
	deleteErr  error
}

func (s *stubKubevirt) Snapshot(vm, snap string) (string, error) {
	if s.snapErr != nil {
		return "", s.snapErr
	}
	if snap != "" {
		return snap, nil
	}
	return s.snapName, nil
}

func (s *stubKubevirt) ListSnapshots(vm string) ([]kubevirt.SnapshotInfo, error) {
	return s.infos, s.listErr
}

func (s *stubKubevirt) RestoreSnapshot(vm, snap string) error {
	s.restored = snap
	return s.restoreErr
}

func (s *stubKubevirt) DeleteSnapshot(snap string) error {
	s.deleted = snap
	return s.deleteErr
}

func withKubevirtStub(t *testing.T, stub kubevirtSnapshooter) {
	t.Helper()
	prev := kubevirtClientFor
	kubevirtClientFor = func(types.InstanceRef) kubevirtSnapshooter { return stub }
	t.Cleanup(func() { kubevirtClientFor = prev })
}

func withProxmoxError(t *testing.T, err error) {
	t.Helper()
	prev := ProxmoxClientFor
	ProxmoxClientFor = func(types.InstanceRef) (*proxmoxbe.Client, error) { return nil, err }
	t.Cleanup(func() { ProxmoxClientFor = prev })
}

// The consistency-label matrix: what each backend promises about a snapshot
// taken in each power state. One table so a label change in any adapter shows
// up as a contract diff rather than a silent drift.
func TestBackfill_ConsistencyMatrix(t *testing.T) {
	t.Run("kubevirt always filesystem", func(t *testing.T) {
		withKubevirtStub(t, &stubKubevirt{snapName: "auto"})
		snap, err := (KubeVirt{}).Create(types.InstanceRef{Backend: "kubevirt", Name: "web"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if snap.Consistency != Filesystem {
			t.Errorf("consistency = %q, want Filesystem: KubeVirt quiesces the guest", snap.Consistency)
		}
	})

	t.Run("libvirt", func(t *testing.T) {
		for _, tc := range []struct {
			state string
			want  Consistency
		}{
			{"running", Crash},
			{"shut off", Offline},
		} {
			fake := withFake(t)
			fake.AddResponse("virsh -c qemu:///system domstate web", tc.state, nil)
			fake.AddResponse("virsh -c qemu:///system snapshot-create-as --domain web --name s1 --atomic", "", nil)
			snap, err := (Libvirt{}).Create(libvirtRef("web"), "s1")
			if err != nil {
				t.Fatal(err)
			}
			if snap.Consistency != tc.want {
				t.Errorf("state %s: consistency = %q, want %q", tc.state, snap.Consistency, tc.want)
			}
		}
	})

	t.Run("incus", func(t *testing.T) {
		for _, tc := range []struct {
			status string
			want   Consistency
		}{
			{"Running", Crash},
			{"Stopped", Offline},
		} {
			fake := withFake(t)
			incus.SetRunner(fake)
			t.Cleanup(func() { incus.SetRunner(shell.Real{}) })
			fake.AddResponse("incus snapshot create lab:web s1", "", nil)
			fake.AddResponse("incus list lab: --format=json",
				`[{"name":"web","type":"virtual-machine","status":`+`"`+tc.status+`"}]`, nil)
			snap, err := (Incus{}).Create(incusRef("web"), "s1")
			if err != nil {
				t.Fatal(err)
			}
			if snap.Consistency != tc.want {
				t.Errorf("status %s: consistency = %q, want %q", tc.status, snap.Consistency, tc.want)
			}
		}
	})

	t.Run("qemu stopped is offline", func(t *testing.T) {
		home := t.TempDir()
		qemuVMDir(t, home, "dev")
		fake := withFake(t)
		fake.AddPrefixResponse("/fake/bin/qemu-img info", `{"format":"qcow2"}`, nil)
		fake.AddPrefixResponse("/fake/bin/qemu-img snapshot -c", "", nil)
		stoppedVM(t)

		snap, err := (QEMU{}).Create(types.InstanceRef{Backend: "qemu", Name: "dev"}, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if snap.Consistency != Offline {
			t.Errorf("consistency = %q, want Offline: local snapshots only happen stopped", snap.Consistency)
		}
	})

	t.Run("virtualbox", func(t *testing.T) {
		for _, tc := range []struct {
			state string
			want  Consistency
		}{
			{"running", Crash},
			{"poweroff", Offline},
		} {
			fake := withFake(t)
			fake.AddResponse("VBoxManage showvminfo web --machinereadable", "VMState=\""+tc.state+"\"\n", nil)
			fake.AddResponse("VBoxManage snapshot web take s1", "", nil)
			snap, err := (VirtualBox{}).Create(virtualboxRef("web"), "s1")
			if err != nil {
				t.Fatal(err)
			}
			if snap.Consistency != tc.want {
				t.Errorf("state %s: consistency = %q, want %q", tc.state, snap.Consistency, tc.want)
			}
		}
	})

	t.Run("proxmox", func(t *testing.T) {
		p := Proxmox{}
		for _, tc := range []struct {
			running, memory bool
			want            Consistency
		}{
			{false, false, Offline},
			{true, true, Filesystem},
			{true, false, Crash},
		} {
			if got := p.consistency(tc.running, tc.memory); got != tc.want {
				t.Errorf("consistency(%v,%v) = %q, want %q", tc.running, tc.memory, got, tc.want)
			}
		}
		// toSnapshot carries the label through, not just computes it.
		got := p.toSnapshot(types.InstanceRef{Backend: "proxmox", Name: "vm"},
			proxmoxbe.SnapshotInfo{Name: "s1", SnapTime: 1750000000, VMState: 1, Running: 1})
		if got.Consistency != Filesystem || !got.Ready || got.Created == "" {
			t.Errorf("toSnapshot dropped the capture: %+v", got)
		}
	})
}

func TestBackfill_KubeVirt_ListRestoreDelete(t *testing.T) {
	stub := &stubKubevirt{infos: []kubevirt.SnapshotInfo{
		{Name: "older", Source: "web", Created: "2026-01-01T00:00:00Z", Ready: true},
		{Name: "newer", Source: "web", Created: "2026-01-02T00:00:00Z", Ready: false},
	}}
	withKubevirtStub(t, stub)
	ref := types.InstanceRef{Backend: "kubevirt", Name: "web"}

	snaps, err := (KubeVirt{}).List(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].Name != "newer" {
		t.Fatalf("list should sort newest first: %+v", snaps)
	}
	for _, s := range snaps {
		if s.Consistency != Filesystem {
			t.Errorf("%s: consistency = %q, want Filesystem", s.Name, s.Consistency)
		}
	}

	if err := (KubeVirt{}).Restore(ref, "newer"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if stub.restored != "newer" {
		t.Errorf("restored = %q", stub.restored)
	}
	if err := (KubeVirt{}).Delete(ref, "older"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if stub.deleted != "older" {
		t.Errorf("deleted = %q", stub.deleted)
	}

	boom := errors.New("cluster gone")
	withKubevirtStub(t, &stubKubevirt{snapErr: boom, listErr: boom, restoreErr: boom, deleteErr: boom})
	if _, err := (KubeVirt{}).Create(ref, "s"); !errors.Is(err, boom) {
		t.Errorf("Create should propagate client errors, got %v", err)
	}
	if _, err := (KubeVirt{}).List(ref); !errors.Is(err, boom) {
		t.Errorf("List should propagate client errors, got %v", err)
	}
	if err := (KubeVirt{}).Restore(ref, "s"); !errors.Is(err, boom) {
		t.Errorf("Restore should propagate client errors, got %v", err)
	}
	if err := (KubeVirt{}).Delete(ref, "s"); !errors.Is(err, boom) {
		t.Errorf("Delete should propagate client errors, got %v", err)
	}
}

func TestBackfill_Libvirt_Restore(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("virsh -c qemu:///system snapshot-revert --domain web --snapshotname s1", "", nil)
	if err := (Libvirt{}).Restore(libvirtRef("web"), "s1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	fake.AddResponse("virsh -c qemu:///system snapshot-revert --domain web --snapshotname bad",
		"error: no domain snapshot", errors.New("exit 1"))
	err := (Libvirt{}).Restore(libvirtRef("web"), "bad")
	if err == nil || !strings.Contains(err.Error(), "no domain snapshot") {
		t.Fatalf("virsh's explanation was dropped: %v", err)
	}
}

func TestBackfill_Libvirt_List_LabelsAndTimes(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("virsh -c qemu:///system snapshot-list --domain web --name", "s1\ns2\n", nil)
	fake.AddResponse("virsh -c qemu:///system snapshot-info --domain web --snapshotname s1",
		"Name: s1\nCreation time: 2026-01-02 10:00:00 +0000\n", nil)
	fake.AddResponse("virsh -c qemu:///system snapshot-info --domain web --snapshotname s2",
		"", errors.New("exit 1"))

	snaps, err := (Libvirt{}).List(libvirtRef("web"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %+v", snaps)
	}
	for _, s := range snaps {
		if s.Consistency != Crash {
			t.Errorf("%s: consistency = %q, want Crash: virsh cannot quiesce unconditionally", s.Name, s.Consistency)
		}
	}
	byName := map[string]Snapshot{}
	for _, s := range snaps {
		byName[s.Name] = s
	}
	if byName["s1"].Created != "2026-01-02 10:00:00 +0000" {
		t.Errorf("s1.Created = %q", byName["s1"].Created)
	}
	if byName["s2"].Created != "" {
		t.Errorf("s2.Created should be empty when virsh won't say, got %q", byName["s2"].Created)
	}
}

func TestBackfill_Incus_RestoreDelete(t *testing.T) {
	fake := withFake(t)
	// Restore checks the instance is stopped through the incus package.
	instances := shell.NewFake()
	instances.AddResponse("incus list lab: --format=json", `[{"name":"web","status":"Stopped"}]`, nil)
	incus.SetRunner(instances)
	t.Cleanup(func() { incus.SetRunner(shell.Real{}) })
	fake.AddResponse("incus snapshot restore lab:web s1", "", nil)
	if err := (Incus{}).Restore(incusRef("web"), "s1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	fake.AddResponse("incus snapshot restore lab:web bad",
		"Error: not found", errors.New("exit 1"))
	if err := (Incus{}).Restore(incusRef("web"), "bad"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("restore failure was dropped: %v", err)
	}

	fake.AddResponse("incus snapshot delete lab:web gone",
		"Error: not found", errors.New("exit 1"))
	if err := (Incus{}).Delete(incusRef("web"), "gone"); err != nil {
		t.Errorf("deleting a gone snapshot should be nil, got %v", err)
	}
}

func TestBackfill_QEMU_CreateListRestoreDelete(t *testing.T) {
	home := t.TempDir()
	qemuVMDir(t, home, "dev")
	fake := withFake(t)
	fake.AddPrefixResponse("/fake/bin/qemu-img info",
		`{"format":"qcow2","snapshots":[{"name":"s1","date-sec":1750000000,"date-nsec":0}]}`, nil)
	fake.AddPrefixResponse("/fake/bin/qemu-img snapshot -c", "", nil)
	fake.AddPrefixResponse("/fake/bin/qemu-img snapshot -a", "", nil)
	stoppedVM(t)
	ref := types.InstanceRef{Backend: "qemu", Name: "dev"}

	snap, err := (QEMU{}).Create(ref, "s1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if snap.Consistency != Offline || snap.Name != "s1" {
		t.Errorf("got %+v", snap)
	}

	snaps, err := (QEMU{}).List(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Consistency != Offline {
		t.Fatalf("got %+v", snaps)
	}
	if !strings.HasPrefix(snaps[0].Created, "2025-") {
		t.Errorf("Created should render date-sec, got %q", snaps[0].Created)
	}

	if err := (QEMU{}).Restore(ref, "s1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	fake.AddPrefixResponse("/fake/bin/qemu-img snapshot -d", "can't find snapshot", errors.New("exit 1"))
	if err := (QEMU{}).Delete(ref, "gone"); err != nil {
		t.Errorf("deleting a gone snapshot should be nil, got %v", err)
	}
}

func TestBackfill_QEMU_SurfacesToolErrors(t *testing.T) {
	home := t.TempDir()
	qemuVMDir(t, home, "dev")
	fake := withFake(t)
	fake.AddPrefixResponse("/fake/bin/qemu-img info", `{"format":"qcow2"}`, nil)
	fake.AddPrefixResponse("/fake/bin/qemu-img snapshot -c",
		"qemu-img: error while writing", errors.New("exit 1"))
	stoppedVM(t)

	_, err := (QEMU{}).Create(types.InstanceRef{Backend: "qemu", Name: "dev"}, "s1")
	if err == nil || !strings.Contains(err.Error(), "error while writing") {
		t.Fatalf("qemu-img's explanation was dropped: %v", err)
	}

	fake.AddPrefixResponse("/fake/bin/qemu-img info", "not json", nil)
	if _, err := (QEMU{}).List(types.InstanceRef{Backend: "qemu", Name: "dev"}); err == nil {
		t.Fatal("unreadable qemu-img output should error")
	}
}

func TestBackfill_Proxmox_ClientErrors(t *testing.T) {
	boom := errors.New("no proxmox context configured")
	withProxmoxError(t, boom)
	ref := types.InstanceRef{Backend: "proxmox", Name: "vm"}

	if _, err := (Proxmox{}).Create(ref, "s1"); !errors.Is(err, boom) {
		t.Errorf("Create should propagate client errors, got %v", err)
	}
	if _, err := (Proxmox{}).List(ref); !errors.Is(err, boom) {
		t.Errorf("List should propagate client errors, got %v", err)
	}
	if err := (Proxmox{}).Restore(ref, "s1"); !errors.Is(err, boom) {
		t.Errorf("Restore should propagate client errors, got %v", err)
	}
	if err := (Proxmox{}).Delete(ref, "s1"); !errors.Is(err, boom) {
		t.Errorf("Delete should propagate client errors, got %v", err)
	}
}

func TestBackfill_Proxmox_ToSnapshot(t *testing.T) {
	p := Proxmox{}
	ref := types.InstanceRef{Backend: "proxmox", Name: "vm"}

	got := p.toSnapshot(ref, proxmoxbe.SnapshotInfo{Name: "off", SnapTime: 1750000000})
	if got.Consistency != Offline || !got.Ready || got.Source != "vm" {
		t.Errorf("stopped capture: %+v", got)
	}
	got = p.toSnapshot(ref, proxmoxbe.SnapshotInfo{Name: "mem", SnapTime: 1750000000, VMState: 1, Running: 1})
	if got.Consistency != Filesystem {
		t.Errorf("memory-inclusive capture should be Filesystem: %+v", got)
	}
	got = p.toSnapshot(ref, proxmoxbe.SnapshotInfo{Name: "live", Running: 1})
	if got.Consistency != Crash || got.Created != "" {
		t.Errorf("running capture without memory: %+v", got)
	}
}

func TestBackfill_SnapshotError(t *testing.T) {
	withRemedy := &Unsupported{Backend: "qemu", Reason: "raw disks", Remedy: "recreate from qcow2"}
	if msg := withRemedy.Error(); !strings.Contains(msg, "qemu") || !strings.Contains(msg, "recreate") {
		t.Errorf("Error should name backend and remedy: %q", msg)
	}
	bare := &Unsupported{Backend: "vmware", Reason: "unknown"}
	if msg := bare.Error(); !strings.Contains(msg, "vmware") || strings.Contains(msg, "—") {
		t.Errorf("bare Error: %q", msg)
	}
}
