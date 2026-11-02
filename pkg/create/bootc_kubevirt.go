package create

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) runKubevirtBootcCreate(name string) error {
	backend.ActivateContext(types.InstanceRef{Backend: "kubevirt", Context: r.createContext(config.KubeContext)})
	ns := r.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	sshKey := kubevirt.LoadSSHPublicKey()
	if sshKey == "" {
		return fmt.Errorf("no SSH public key found (needed for bootc VM)")
	}
	size := r.Disk
	if size == "" {
		size = "80Gi"
	}
	placement, err := r.createPlacement()
	if err != nil {
		return err
	}
	// Refuse before the build: it takes minutes, and its VM would never
	// schedule.
	if placement.DedicatedCPUs {
		if err := kubevirt.CheckDedicatedCPUs(r.Node); err != nil {
			return err
		}
	}

	// The bootc build pipeline is an optional plugin (kept out of the lean core
	// binary — see pkg/kubevirt/bootc_core.go). When it isn't compiled in,
	// delegate the build+VM creation to the `corral-bootc` plugin so
	// `corral create --bootc ... --wait-ssh` still works as one command. The
	// plugin creates the VM stopped; we then handle --start/--wait-ssh below.
	if !kubevirt.BootcAvailable() {
		if !plugin.IsInstalled("bootc") {
			return fmt.Errorf("bootc build needs the bootc plugin: corral plugin install bootc")
		}
		if err := plugin.Dispatch("bootc", r.bootcPluginArgs(name, ns, size)); err != nil {
			return err
		}
		if r.Registry != nil {
			if err := r.Registry.SetRef(types.InstanceRef{Backend: "kubevirt", Context: r.createContext(config.KubeContext), Namespace: ns, Name: name}, types.RegistryEntry{}); err != nil {
				return err
			}
		}
		if r.WaitSSH {
			fmt.Fprintf(os.Stderr, "  Waiting for SSH (timeout %ds)...\n", r.Timeout)
			return kubevirt.NewClient(ns).WaitSSH(name, r.SSHUser, "", time.Duration(r.Timeout)*time.Second)
		}
		if r.Start {
			return kubevirt.NewClient(ns).StartVM(name)
		}
		return nil
	}

	build, err := kubevirt.BootcBuildDisk(name, ns, r.Bootc, sshKey, size, r.StorageClass, r.ProvisionScript, r.Kargs, r.Node, os.Stderr)
	if err != nil {
		return err
	}
	vm := kubevirt.GenerateBootcVM(name, ns, build.PVCName, r.Bootc, sshKey, r.Mem, r.CPU, r.Node)
	kubevirt.ApplyPlacement(vm, placement)
	if err := kubevirt.Apply(vm); err != nil {
		return err
	}
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}
	if r.Registry != nil {
		if err := r.Registry.SetRef(types.InstanceRef{Backend: "kubevirt", Context: r.createContext(config.KubeContext), Namespace: ns, Name: name}, types.RegistryEntry{}); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s\n", name, ns)
	fmt.Fprintf(os.Stderr, "  Start:  corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  SSH:    corral ssh %s\n", name)

	// --wait-ssh turns create into a boot gate on KubeVirt too: start the VM
	// and block until root SSH answers, so the same command works on both
	// backends. --start (without --wait-ssh) just brings it up.
	if r.WaitSSH {
		fmt.Fprintf(os.Stderr, "  Waiting for SSH (timeout %ds)...\n", r.Timeout)
		return kubevirt.NewClient(ns).WaitSSH(name, r.SSHUser, "", time.Duration(r.Timeout)*time.Second)
	}
	if r.Start {
		return kubevirt.NewClient(ns).StartVM(name)
	}
	return nil
}

// bootcPluginArgs is the `corral bootc` command line that delegates a
// KubeVirt bootc create to the plugin when the build is not compiled in.
func (r request) bootcPluginArgs(name, ns, size string) []string {
	args := []string{"create", name, "--image", r.Bootc, "-n", ns,
		"--cpu", strconv.Itoa(r.CPU), "--mem", r.Mem}
	if size != "" {
		args = append(args, "--disk", size)
	}
	if r.StorageClass != "" {
		args = append(args, "--storage-class", r.StorageClass)
	}
	if r.Node != "" {
		args = append(args, "--node", r.Node)
	}
	for _, karg := range r.Kargs {
		args = append(args, "--karg", karg)
	}
	if r.DedicatedCPUs {
		args = append(args, "--dedicated-cpus")
	}
	if r.IsolateEmulator {
		args = append(args, "--isolate-emulator-thread")
	}
	for _, tag := range r.Tags {
		args = append(args, "--tag", tag)
	}
	return args
}
