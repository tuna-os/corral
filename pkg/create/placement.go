package create

import (
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

// createPlacement parses --dedicated-cpus, --isolate-emulator-thread, and
// --tag. They are KubeVirt-only: Execute refuses them elsewhere.
func (r request) createPlacement() (kubevirt.Placement, error) {
	tags, err := kubevirt.ParseTags(r.Tags)
	if err != nil {
		return kubevirt.Placement{}, err
	}
	p := kubevirt.Placement{
		DedicatedCPUs:         r.DedicatedCPUs,
		IsolateEmulatorThread: r.IsolateEmulator,
		Tags:                  tags,
	}
	if err := p.Validate(); err != nil {
		return kubevirt.Placement{}, err
	}
	if p.DedicatedCPUs && r.InstanceType != "" {
		return kubevirt.Placement{}, fmt.Errorf("--dedicated-cpus cannot combine with --instancetype: the instancetype owns the CPU spec, so pick one that sets dedicatedCPUPlacement instead")
	}
	return p, nil
}

// refuseKubevirtOnlyFlags refuses the placement flags on a backend that
// cannot honour them, rather than creating a VM without them.
func (r request) refuseKubevirtOnlyFlags(backendName string) error {
	var set []string
	if r.DedicatedCPUs {
		set = append(set, "--dedicated-cpus")
	}
	if r.IsolateEmulator {
		set = append(set, "--isolate-emulator-thread")
	}
	if len(r.Tags) > 0 {
		set = append(set, "--tag")
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s: supported on the KubeVirt backend only, not on backend %s", strings.Join(set, ", "), backendName)
}
