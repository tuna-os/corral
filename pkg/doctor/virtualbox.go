package doctor

// VirtualBox context checks.
//
// A local VirtualBox context has two ways to be wrong: VBoxManage is missing,
// or it does not answer. The second covers an unusable driver as well as a
// missing one — `list vms` talks to the VirtualBox service, so success means
// the whole local stack works.
func virtualboxChecks() []Check {
	if _, err := runner.LookPath("VBoxManage"); err != nil {
		return []Check{{Name: "VBoxManage CLI", Detail: "VBoxManage is not installed", Severity: "required"}}
	}
	out, err := runner.Run("VBoxManage", "list", "vms")
	reachable := err == nil
	return []Check{{Name: "VirtualBox reachable", OK: reachable, Severity: "required", Detail: detailIf(reachable, "VBoxManage answers and the local VirtualBox service is usable", commandFailure(out, err, "verify VirtualBox is installed and its kernel modules are loaded"))}}
}
