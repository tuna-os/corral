//go:build bootc

// One-off shim: finish a bootc create whose builder completed but whose
// final-VM step was lost (see the waitForBuilderVM post-shutdown race).
package main

import (
	"fmt"
	"os"

	"github.com/tuna-os/corral/pkg/kubevirt"
)

func main() {
	name, ns, pvc, img := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	vm := kubevirt.GenerateBootcVM(name, ns, pvc, img, kubevirt.LoadSSHPublicKey(), "4G", 2, "")
	if vm == nil {
		panic("bootc plugin not compiled in")
	}
	if err := kubevirt.Apply(vm); err != nil {
		panic(err)
	}
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintln(os.Stderr, "proxy warn:", err)
	}
	fmt.Println("final VM created:", name)
}
