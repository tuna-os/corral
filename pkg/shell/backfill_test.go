package shell

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// markerRunner fails fast with canned output — no sleeps, so marker-based
// timeout classification is exercised deterministically.
type markerRunner struct {
	out   string
	err   error
	calls int
}

func (m *markerRunner) Run(name string, args ...string) ([]byte, error) {
	m.calls++
	return []byte(m.out), m.err
}

func (m *markerRunner) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	m.calls++
	return []byte(m.out), m.err
}

func (m *markerRunner) LookPath(name string) (string, error) { return "/fake/bin/" + name, nil }

func TestBackfill_Real_Run(t *testing.T) {
	var r Real
	if out, err := r.Run("true"); err != nil {
		t.Fatalf("true should succeed: %v (out %q)", err, out)
	}
	if _, err := r.Run("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("missing binary should error")
	}
	if out, err := r.Run("sh", "-c", "echo out; echo err >&2; exit 3"); err == nil {
		t.Fatalf("failing exit should error, out=%q", out)
	} else if !strings.Contains(string(out), "out") {
		t.Errorf("combined output should include stdout, got %q", out)
	}
}

func TestBackfill_Real_RunStdin(t *testing.T) {
	var r Real
	out, err := r.RunStdin("hello-backfill", "cat")
	if err != nil {
		t.Fatalf("cat should echo stdin: %v", err)
	}
	if string(out) != "hello-backfill" {
		t.Errorf("got %q, want %q", out, "hello-backfill")
	}
	if _, err := r.RunStdin("x", "definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("missing binary should error")
	}
}

func TestBackfill_Real_LookPath(t *testing.T) {
	var r Real
	if p, err := r.LookPath("sh"); err != nil || p == "" {
		t.Fatalf("sh should resolve: %v %q", err, p)
	}
	if _, err := r.LookPath("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("missing binary should error")
	}
}

func TestBackfill_WithKubeContext(t *testing.T) {
	inner := NewFake()
	// Empty context returns the wrapped runner untouched.
	if got := WithKubeContext(inner, ""); got != Runner(inner) {
		t.Error("empty context should return the inner runner")
	}

	inner.AddResponse("kubectl --context=prod get pods", "ok", nil)
	r := WithKubeContext(inner, "prod")
	if out, err := r.Run("kubectl", "get", "pods"); err != nil || string(out) != "ok" {
		t.Fatalf("kubectl should gain --context: %v %q", err, out)
	}

	// virtctl is context-bound too; other binaries are not.
	inner.AddResponse("virtctl --context=prod start vm", "started", nil)
	if _, err := r.Run("virtctl", "start", "vm"); err != nil {
		t.Fatalf("virtctl should gain --context: %v", err)
	}
	inner.AddResponse("systemctl start foo", "done", nil)
	if _, err := r.Run("systemctl", "start", "foo"); err != nil {
		t.Fatalf("non-k8s binary must pass through: %v", err)
	}

	// Basename matching covers absolute paths.
	inner.AddResponse("kubectl --context=prod get vms", "vms", nil)
	if _, err := r.Run("/usr/bin/kubectl", "get", "vms"); err != nil {
		// commandKey uses the full name, so register the exact key too.
		inner.AddResponse("/usr/bin/kubectl --context=prod get vms", "vms", nil)
		if _, err2 := r.Run("/usr/bin/kubectl", "get", "vms"); err2 != nil {
			t.Fatalf("absolute kubectl path should gain --context: %v / %v", err, err2)
		}
	}

	// Pre-existing context (both forms) must not be duplicated.
	inner.AddResponse("kubectl --context=other get pods", "other", nil)
	if _, err := r.Run("kubectl", "--context=other", "get", "pods"); err != nil {
		t.Fatalf("existing --context must be kept: %v", err)
	}
	inner.AddResponse("kubectl --context other get pods", "other2", nil)
	if _, err := r.Run("kubectl", "--context", "other", "get", "pods"); err != nil {
		t.Fatalf("existing --context separate form must be kept: %v", err)
	}

	// RunStdin and LookPath delegate through the wrapper.
	inner.AddResponse("kubectl --context=prod apply -f -", "created", nil)
	if out, err := r.RunStdin("yaml", "kubectl", "apply", "-f", "-"); err != nil || string(out) != "created" {
		t.Fatalf("RunStdin should inject context: %v %q", err, out)
	}
	if p, err := r.LookPath("kubectl"); err != nil || p == "" {
		t.Fatalf("LookPath should delegate: %v %q", err, p)
	}
}

func TestBackfill_Command(t *testing.T) {
	t.Setenv("CORRAL_KUBE_CONTEXT", "envctx")
	cmd := Command("kubectl", "get", "pods")
	found := false
	for _, a := range cmd.Args {
		if a == "--context=envctx" {
			found = true
		}
	}
	if !found {
		t.Errorf("Command should inject env context, args=%v", cmd.Args)
	}

	// Non-k8s binaries are untouched.
	cmd = Command("systemctl", "start", "x")
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--context") {
			t.Errorf("non-k8s command must not gain context: %v", cmd.Args)
		}
	}

	// Explicit context wins; no duplication.
	cmd = Command("kubectl", "--context=other", "get", "pods")
	n := 0
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--context") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly one --context, args=%v", cmd.Args)
	}

	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	cmd = Command("kubectl", "get", "pods")
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--context") {
			t.Errorf("empty env must not inject: %v", cmd.Args)
		}
	}
}

func TestBackfill_CommandForContext(t *testing.T) {
	cmd := CommandForContext("prod", "kubectl", "get", "pods")
	if cmd.Args[1] != "--context=prod" {
		t.Errorf("context should be prepended, args=%v", cmd.Args)
	}
	// Empty context: untouched.
	cmd = CommandForContext("", "kubectl", "get", "pods")
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--context") {
			t.Errorf("empty context must not inject: %v", cmd.Args)
		}
	}
	// Existing context: untouched.
	cmd = CommandForContext("prod", "kubectl", "--context=other", "get", "pods")
	n := 0
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--context") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly one --context, args=%v", cmd.Args)
	}
	// Non-k8s binary: untouched even with context set.
	cmd = CommandForContext("prod", "systemctl", "start", "x")
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--context") {
			t.Errorf("non-k8s must not gain context: %v", cmd.Args)
		}
	}
}

func TestBackfill_KubectlTimeoutDefault(t *testing.T) {
	t.Setenv("CORRAL_KUBECTL_TIMEOUT", "")
	if got := kubectlTimeoutDefault(); got != "5s" {
		t.Errorf("default should be 5s, got %q", got)
	}
	t.Setenv("CORRAL_KUBECTL_TIMEOUT", "30s")
	if got := kubectlTimeoutDefault(); got != "30s" {
		t.Errorf("env override should win, got %q", got)
	}
	t.Setenv("CORRAL_KUBECTL_TIMEOUT", "not-a-duration")
	if got := kubectlTimeoutDefault(); got != "5s" {
		t.Errorf("invalid env should fall back to 5s, got %q", got)
	}
}

func TestBackfill_Inject_RespectsExistingTimeout(t *testing.T) {
	fake := NewFake()
	fake.AddResponse("kubectl --request-timeout=7s get pods", "ok", nil)
	r := WithKubectlTimeout(fake, "10s")
	out, err := r.Run("kubectl", "--request-timeout=7s", "get", "pods")
	if err != nil || string(out) != "ok" {
		t.Fatalf("existing timeout flag must be kept: %v %q", err, out)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Fatalf("want 1 call, got %d", n)
	}
	if got := fake.Calls()[0].Name + " " + strings.Join(fake.Calls()[0].Args, " "); strings.Contains(got, "--request-timeout=10s") {
		t.Errorf("must not double-inject timeout, got %q", got)
	}
}

func TestBackfill_InjectKubectlContext(t *testing.T) {
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	if got := injectKubectlContext([]string{"get", "pods"}); len(got) != 2 {
		t.Errorf("empty env must not inject: %v", got)
	}
	t.Setenv("CORRAL_KUBE_CONTEXT", "envctx")
	got := injectKubectlContext([]string{"get", "pods"})
	if len(got) != 3 || got[0] != "--context=envctx" {
		t.Errorf("env context should be prepended: %v", got)
	}
	got = injectKubectlContext([]string{"--context=other", "get", "pods"})
	if len(got) != 3 || got[0] != "--context=other" {
		t.Errorf("existing context must be kept: %v", got)
	}
}

func TestBackfill_LooksLikeTimeout_Markers(t *testing.T) {
	boom := errors.New("exit status 1")
	for _, marker := range []string{
		"Unable to connect to the server",
		"connection refused",
		"no route to host",
		"i/o timeout",
		"context deadline exceeded",
		"Client.Timeout",
	} {
		m := &markerRunner{out: "Error: " + marker, err: boom}
		r := WithKubectlTimeout(m, "5s") // large budget: only the marker can trip
		if _, err := r.Run("kubectl", "get", "pods"); err == nil {
			t.Fatalf("marker %q should error", marker)
		}
		if m.calls != 1 {
			t.Fatalf("marker %q: want 1 call, got %d", marker, m.calls)
		}
		start := time.Now()
		_, err := r.Run("kubectl", "get", "nodes")
		if err == nil || !strings.Contains(err.Error(), "cluster unreachable") {
			t.Errorf("marker %q should trip breaker: %v", marker, err)
		}
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Errorf("marker %q: blocked call took %v", marker, d)
		}
	}

	// Fast generic failure must NOT trip the breaker.
	m := &markerRunner{out: "Error: some validation problem", err: boom}
	r := WithKubectlTimeout(m, "5s")
	if _, err := r.Run("kubectl", "get", "pods"); err == nil {
		t.Fatal("should error")
	}
	if _, err := r.Run("kubectl", "get", "nodes"); err != boom {
		t.Errorf("generic error must not trip breaker, got %v (calls=%d)", err, m.calls)
	}
	if m.calls != 2 {
		t.Errorf("want 2 passthrough calls, got %d", m.calls)
	}
}

func TestBackfill_ReadOnlyVerb_Matrix(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"get", "pods"}, true},
		{[]string{"top", "node"}, true},
		{[]string{"version"}, true},
		{[]string{"api-resources"}, true},
		{[]string{"api-versions"}, true},
		{[]string{"auth", "can-i"}, true},
		{[]string{"explain", "pod"}, true},
		{[]string{"-n=ns", "get", "pods"}, true}, // leading flags skipped
		{[]string{"delete", "pod", "x"}, false},
		{[]string{"apply", "-f", "-"}, false},
		{[]string{"exec", "pod", "--", "sh"}, false},
		{[]string{"logs", "pod"}, false},
		{[]string{}, false},
		{[]string{"-n", "ns", "get", "pods"}, false}, // flag values are not skipped: first bare word is "ns"
	}
	for _, c := range cases {
		if got := readOnlyVerb(c.args); got != c.want {
			t.Errorf("readOnlyVerb(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestBackfill_KubectlTimeout_RunStdin(t *testing.T) {
	fake := NewFake()
	fake.AddResponse("kubectl --request-timeout=10s get pods", "ok", nil)
	r := WithKubectlTimeout(fake, "10s")
	if out, err := r.RunStdin("", "kubectl", "get", "pods"); err != nil || string(out) != "ok" {
		t.Fatalf("read via RunStdin should inject timeout: %v %q", err, out)
	}

	// Non-kubectl RunStdin passes through untouched.
	fake.AddResponse("virtctl ssh vm", "shell", nil)
	if _, err := r.RunStdin("in", "virtctl", "ssh", "vm"); err != nil {
		t.Fatalf("non-kubectl RunStdin must pass through: %v", err)
	}

	// Mutations pass through without the timeout flag.
	fake.AddResponse("kubectl apply -f -", "created", nil)
	if out, err := r.RunStdin("yaml", "kubectl", "apply", "-f", "-"); err != nil || string(out) != "created" {
		t.Fatalf("mutation RunStdin must bypass injection: %v %q", err, out)
	}

	// Tripped breaker blocks RunStdin reads fast too.
	slow := WithKubectlTimeout(slowRunner{delay: 60 * time.Millisecond}, "50ms")
	if _, err := slow.Run("kubectl", "get", "vms"); err == nil {
		t.Fatal("slow call should error")
	}
	start := time.Now()
	_, err := slow.RunStdin("", "kubectl", "get", "nodes")
	if err == nil || !strings.Contains(err.Error(), "cluster unreachable") {
		t.Fatalf("tripped breaker should block RunStdin: %v", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("blocked RunStdin took %v, want instant", d)
	}

	// LookPath delegates through the timeout wrapper.
	if p, err := r.LookPath("kubectl"); err != nil || p == "" {
		t.Fatalf("LookPath should delegate: %v %q", err, p)
	}
}

func TestBackfill_Call_SuccessClearsBreaker(t *testing.T) {
	boom := errors.New("boom: Unable to connect to the server")
	m := &markerRunner{out: "Unable to connect to the server", err: boom}
	r := WithKubectlTimeout(m, "5s").(*kubectlTimeoutRunner)
	if _, err := r.Run("kubectl", "get", "pods"); err == nil {
		t.Fatal("should error")
	}
	if _, err := r.Run("kubectl", "get", "nodes"); !strings.Contains(err.Error(), "cluster unreachable") {
		t.Fatalf("breaker should be tripped: %v", err)
	}
	// Simulate TTL expiry, then a success must clear the breaker state.
	r.mu.Lock()
	r.deadUntil = time.Now().Add(-time.Second)
	r.mu.Unlock()
	m.err = nil
	m.out = "recovered"
	if out, err := r.Run("kubectl", "get", "pods"); err != nil || string(out) != "recovered" {
		t.Fatalf("success after expiry should pass: %v %q", err, out)
	}
	r.mu.Lock()
	cleared := r.deadUntil.IsZero()
	r.mu.Unlock()
	if !cleared {
		t.Error("success should clear deadUntil")
	}
}

func TestBackfill_HardRun(t *testing.T) {
	r := WithKubectlTimeout(NewFake(), "50ms").(*kubectlTimeoutRunner)
	out, err := r.hardRun("", "echo", []string{"hi"})
	if err != nil || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("echo should succeed: %v %q", err, out)
	}
	out, err = r.hardRun("piped", "cat", nil)
	if err != nil || string(out) != "piped" {
		t.Fatalf("cat should echo stdin: %v %q", err, out)
	}
	if _, err := r.hardRun("", "definitely-not-a-real-binary-xyz", nil); err == nil {
		t.Fatal("missing binary should error")
	}
	if _, err := r.hardRun("", "sh", []string{"-c", "exit 3"}); err == nil {
		t.Fatal("failing exit should error")
	}
	// Invalid timeout budget falls back to a 30s deadline, not zero.
	bad := WithKubectlTimeout(NewFake(), "bogus").(*kubectlTimeoutRunner)
	if out, err := bad.hardRun("", "echo", []string{"ok"}); err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("invalid timeout should still run: %v %q", err, out)
	}
	// Run/RunStdin on a non-kubectl binary skip injection entirely.
	nr := WithKubectlTimeout(NewFake(), "10s")
	f := nr.(*kubectlTimeoutRunner).next.(*Fake)
	f.AddResponse("echo hi", "hi", nil)
	if _, err := nr.Run("echo", "hi"); err != nil {
		t.Fatalf("non-kubectl Run must pass through: %v", err)
	}
	f.AddResponse("echo hi-stdin", "hi-stdin", nil)
	if _, err := nr.RunStdin("in", "echo", "hi-stdin"); err != nil {
		t.Fatalf("non-kubectl RunStdin must pass through: %v", err)
	}
}

func TestBackfill_RunWithSSHPass_Missing(t *testing.T) {
	if _, err := exec.LookPath("sshpass"); err == nil {
		t.Skip("sshpass installed; error path not exercisable")
	}
	if err := RunWithSSHPass("pw", "ssh", "host"); err == nil || !strings.Contains(err.Error(), "sshpass") {
		t.Fatalf("missing sshpass should error helpfully, got %v", err)
	}
}
