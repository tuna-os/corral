package xen

import "testing"

// Exact wire contract per the VM class reference
// (https://xapi-project.github.io/xen-api/classes/vm.html):
// resume(session, VM, start_paused, force).
func TestResumeIssuesResume(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.resume"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Resume("web-1"); err != nil {
		t.Fatal(err)
	}
	assertWire(t, f, "VM.resume", testSession, "OpaqueRef:vm-running", false, false)
}

func TestRebootFallsBackToHardReboot(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.clean_reboot"] = func(string) string {
		return failure("VM_REQUIRES_TOOLS", "OpaqueRef:vm-running")
	}
	f.handlers["VM.hard_reboot"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Reboot("web-1"); err != nil {
		t.Fatal(err)
	}
	if f.served("VM.hard_reboot") != 1 {
		t.Fatal("clean reboot failure did not fall back to hard reboot")
	}
}

// TestRebootCleanNeverForcesHardReboot proves a successful graceful reboot
// issues no hard_reboot.
func TestRebootCleanNeverForcesHardReboot(t *testing.T) {
	f := newFakePool(t)
	f.handlers["session.login_with_password"] = loginHandler(testSession)
	resolveHandlers(f, "OpaqueRef:vm-running")
	f.handlers["VM.clean_reboot"] = func(string) string { return success(`<string></string>`) }
	f.handlers["VM.hard_reboot"] = func(string) string { return success(`<string></string>`) }
	c := testClient(t, f)
	if err := c.Reboot("web-1"); err != nil {
		t.Fatal(err)
	}
	if f.served("VM.clean_reboot") != 1 {
		t.Fatal("Reboot did not issue VM.clean_reboot")
	}
	if f.served("VM.hard_reboot") != 0 {
		t.Fatal("successful clean reboot also issued VM.hard_reboot")
	}
}
