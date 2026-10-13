package launcher

import (
	"context"
	"fmt"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// defaultIsRunningCtx is the WaitVMReady liveness probe: the VM's running
// state, bounded by the wait's own context and absolute deadline, so a hung
// systemctl cannot stall the wait past its budget.
func defaultIsRunningCtx(ctx context.Context, name string, deadline time.Time) bool {
	c, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return qemu.IsRunningCtx(c, name)
}

// defaultDialVNCDeadline dials the VM's loopback VNC endpoint within the
// wait's absolute deadline: a slow connect cannot extend the read budget,
// and cancellation reaches a probe already in flight.
func defaultDialVNCDeadline(ctx context.Context, name string, deadline time.Time) error {
	if !time.Now().Before(deadline) {
		return fmt.Errorf("deadline exceeded")
	}
	addr, err := qemu.LoopbackVNCAddr(name)
	if err != nil {
		return err
	}
	return dialLoopbackVNC(ctx, addr, deadline)
}
