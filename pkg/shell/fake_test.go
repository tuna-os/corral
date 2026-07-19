package shell

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFake_Run_ExactMatch(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("kubectl", []string{"get", "vms", "-o", "json"}, `{"items":[]}`, nil)

	out, err := f.Run("kubectl", "get", "vms", "-o", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"items":[]}` {
		t.Errorf("got %q, want %q", string(out), `{"items":[]}`)
	}
}

func TestFake_Run_NoMatch(t *testing.T) {
	f := NewFake()
	_, err := f.Run("nonexistent", "cmd")
	if err == nil {
		t.Fatal("expected error for unregistered command")
	}
}

func TestFake_RunStdin(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "created", nil)

	out, err := f.RunStdin("some yaml", "kubectl", "apply", "-f", "-")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "created" {
		t.Errorf("got %q, want %q", string(out), "created")
	}
}

func TestFake_Run_PrefixMatch(t *testing.T) {
	f := NewFake()
	f.AddPrefixResponse("kubectl patch vm testvm -n tailvm --type json", "patched", nil)

	// Exact command is longer than the prefix (has -p <json> appended)
	out, err := f.Run("kubectl", "patch", "vm", "testvm", "-n", "tailvm", "--type", "json", "-p", `{"spec":{}}`)
	if err != nil {
		t.Fatalf("prefix match failed: %v", err)
	}
	if string(out) != "patched" {
		t.Errorf("got %q, want %q", string(out), "patched")
	}
}

func TestFake_PrefixLongestWins(t *testing.T) {
	f := NewFake()
	f.AddPrefixResponse("kubectl get", "generic get", nil)
	f.AddPrefixResponse("kubectl get vms", "vms get", nil)

	out, err := f.Run("kubectl", "get", "vms", "-A", "-o", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The longer prefix "kubectl get vms" should win
	if string(out) != "vms get" {
		t.Errorf("got %q, want %q (longest prefix should win)", string(out), "vms get")
	}
}

func TestFake_ExactTakesPriority(t *testing.T) {
	f := NewFake()
	f.AddPrefixResponse("kubectl delete", "prefix match", nil)
	f.AddResponseKV("kubectl", []string{"delete", "vm", "web", "-n", "tailvm"}, "exact match", nil)

	out, err := f.Run("kubectl", "delete", "vm", "web", "-n", "tailvm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Exact match must win over prefix
	if string(out) != "exact match" {
		t.Errorf("got %q, want %q (exact match should beat prefix)", string(out), "exact match")
	}
}

func TestFake_LookPath(t *testing.T) {
	f := NewFake()
	path, err := f.LookPath("virtctl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/fake/bin/virtctl" {
		t.Errorf("got %q, want /fake/bin/virtctl", path)
	}
}

func TestFake_Calls(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("kubectl", []string{"get", "vms"}, "[]", nil)
	f.AddResponseKV("virtctl", []string{"start", "web"}, "started", nil)

	f.Run("kubectl", "get", "vms")
	f.Run("virtctl", "start", "web")

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].Name != "kubectl" || calls[0].Args[0] != "get" {
		t.Errorf("call 0: %s %v", calls[0].Name, calls[0].Args)
	}
	if calls[1].Name != "virtctl" || calls[1].Args[0] != "start" {
		t.Errorf("call 1: %s %v", calls[1].Name, calls[1].Args)
	}
}

func TestFake_Calls_IndependentCopy(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("echo", []string{"hello"}, "hello", nil)
	f.Run("echo", "hello")

	calls1 := f.Calls()
	calls2 := f.Calls()
	// Modify the first copy — should not affect the second
	calls1[0] = Call{Name: "modified"}
	if calls2[0].Name != "echo" {
		t.Error("Calls() did not return independent copy")
	}
}

func TestFake_LastCall(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("cmd1", nil, "", nil)
	f.AddResponseKV("cmd2", nil, "", nil)
	f.AddResponseKV("cmd3", nil, "", nil)

	f.Run("cmd1")
	f.Run("cmd2")
	f.Run("cmd3")

	last := f.LastCall()
	if last == nil {
		t.Fatal("LastCall returned nil")
	}
	if last.Name != "cmd3" {
		t.Errorf("LastCall.Name = %q, want cmd3", last.Name)
	}
}

func TestFake_LastCall_Empty(t *testing.T) {
	f := NewFake()
	if last := f.LastCall(); last != nil {
		t.Errorf("LastCall on empty calls: got %v, want nil", last)
	}
}

func TestFake_Reset(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("kubectl", []string{"get"}, "[]", nil)
	f.Run("kubectl", "get")

	if len(f.Calls()) != 1 {
		t.Fatal("expected 1 call before reset")
	}

	f.Reset()

	if len(f.Calls()) != 0 {
		t.Error("Calls not cleared after Reset")
	}
	if _, err := f.Run("kubectl", "get"); err == nil {
		t.Error("Run with previously-registered command should fail after Reset")
	}
}

func TestFake_ResponseWithError(t *testing.T) {
	f := NewFake()
	customErr := errors.New("kubectl: connection refused")
	f.AddResponseKV("kubectl", []string{"get", "vms"}, `{"items":[]}`, customErr)

	out, err := f.Run("kubectl", "get", "vms")
	if err != customErr {
		t.Errorf("got error %v, want %v", err, customErr)
	}
	// Stdout should still be returned alongside the error
	if string(out) != `{"items":[]}` {
		t.Errorf("got stdout %q, want %q even with error", string(out), `{"items":[]}`)
	}
}

func TestFake_ResponseWithStdoutAndStderr(t *testing.T) {
	f := NewFake()
	// Register a response and modify its Stderr to verify concatenation
	f.AddResponseKV("mycmd", []string{"arg1"}, "out", nil)
	f.mu.Lock()
	r := f.responses[commandKey("mycmd", []string{"arg1"})]
	r.Stderr = "err"
	f.responses[commandKey("mycmd", []string{"arg1"})] = r
	f.mu.Unlock()

	out, err := f.Run("mycmd", "arg1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "outerr" {
		t.Errorf("got %q, want outerr (stdout+stderr)", string(out))
	}
}

func TestFake_AddResponse_String(t *testing.T) {
	f := NewFake()
	f.AddResponse("kubectl get pods", "pod1\npod2", nil)

	out, err := f.Run("kubectl", "get", "pods")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "pod1\npod2" {
		t.Errorf("got %q, want %q", string(out), "pod1\npod2")
	}
}

func TestFake_AddResponseKV_EmptyArgs(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("pwd", nil, "/home/user", nil)

	out, err := f.Run("pwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "/home/user" {
		t.Errorf("got %q, want /home/user", string(out))
	}
}

func TestFake_RunStdin_RecordsCall(t *testing.T) {
	f := NewFake()
	f.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "ok", nil)

	f.RunStdin("yaml content here", "kubectl", "apply", "-f", "-")

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	// RunStdin should still record the call
	if calls[0].Name != "kubectl" {
		t.Errorf("got name %q, want kubectl", calls[0].Name)
	}
}

func TestFake_ConcurrentAccess(t *testing.T) {
	f := NewFake()
	for i := 0; i < 100; i++ {
		f.AddResponseKV(fmt.Sprintf("cmd%d", i), nil, fmt.Sprintf("out%d", i), nil)
	}

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			for j := 0; j < 100; j++ {
				cmd := fmt.Sprintf("cmd%d", j)
				_, err := f.Run(cmd)
				if err != nil {
					t.Errorf("goroutine %d: unexpected error for %s: %v", i, cmd, err)
				}
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	calls := f.Calls()
	if len(calls) != 1000 {
		t.Errorf("expected 1000 calls, got %d", len(calls))
	}
}

func TestFake_MultiplePrefixResponses(t *testing.T) {
	f := NewFake()
	f.AddPrefixResponse("virtctl start", "started", nil)
	f.AddPrefixResponse("virtctl stop", "stopped", nil)

	out, err := f.Run("virtctl", "start", "web", "-n", "tailvm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "started" {
		t.Errorf("got %q, want started", string(out))
	}

	out, err = f.Run("virtctl", "stop", "web", "-n", "tailvm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "stopped" {
		t.Errorf("got %q, want stopped", string(out))
	}
}

func TestCommandKey(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"kubectl", nil, "kubectl "},
		{"kubectl", []string{"get", "pods"}, "kubectl get pods"},
		{"echo", []string{"hello world"}, "echo hello world"},
		{"", nil, " "},
	}
	for _, tt := range tests {
		got := commandKey(tt.name, tt.args)
		if got != tt.want {
			t.Errorf("commandKey(%q, %v) = %q, want %q", tt.name, tt.args, got, tt.want)
		}
	}
}

// slowRunner simulates kubectl hanging until its --request-timeout expires.
type slowRunner struct{ delay time.Duration }

func (s slowRunner) Run(name string, args ...string) ([]byte, error) {
	time.Sleep(s.delay)
	return nil, fmt.Errorf("context deadline exceeded")
}
func (s slowRunner) RunStdin(in, name string, args ...string) ([]byte, error) {
	return s.Run(name, args...)
}
func (s slowRunner) LookPath(name string) (string, error) { return name, nil }

func TestKubectlTimeout_InjectsFlagAndBreaks(t *testing.T) {
	fake := NewFake()
	fake.AddResponse("kubectl --request-timeout=10s get pods", "ok", nil)
	r := WithKubectlTimeout(fake, "10s")
	out, err := r.Run("kubectl", "get", "pods")
	if err != nil || string(out) != "ok" {
		t.Fatalf("flag not injected: %v %q", err, out)
	}
	// Non-kubectl commands pass through untouched.
	fake.AddResponse("virtctl start vm", "started", nil)
	if _, err := r.Run("virtctl", "start", "vm"); err != nil {
		t.Fatalf("non-kubectl call touched: %v", err)
	}

	// A call that burns its full timeout trips the breaker...
	slow := WithKubectlTimeout(slowRunner{delay: 60 * time.Millisecond}, "50ms")
	if _, err := slow.Run("kubectl", "get", "vms"); err == nil {
		t.Fatal("slow call should error")
	}
	// ...so the next call fails instantly instead of waiting again.
	start := time.Now()
	_, err = slow.Run("kubectl", "get", "nodes")
	if err == nil || !strings.Contains(err.Error(), "cluster unreachable") {
		t.Fatalf("breaker not tripped: %v", err)
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("broken call took %v, want instant", d)
	}
}

func TestKubectlTimeout_MutationsBypassDeadlineButRespectBreaker(t *testing.T) {
	fake := NewFake()
	// Mutations pass through untouched — no flag injection.
	fake.AddResponse("kubectl delete pod x -n ns", "pod deleted", nil)
	r := WithKubectlTimeout(fake, "50ms")
	if out, err := r.Run("kubectl", "delete", "pod", "x", "-n", "ns"); err != nil || string(out) != "pod deleted" {
		t.Fatalf("mutation should bypass injection: %v %q", err, out)
	}

	// A tripped breaker (from a read) blocks mutations fast too.
	slow := WithKubectlTimeout(slowRunner{delay: 60 * time.Millisecond}, "50ms")
	slow.Run("kubectl", "get", "vms") // trips
	start := time.Now()
	_, err := slow.Run("kubectl", "delete", "pod", "x")
	if err == nil || !strings.Contains(err.Error(), "cluster unreachable") {
		t.Fatalf("tripped breaker should block mutations: %v", err)
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Error("blocked mutation should fail instantly")
	}
	// But a slow mutation itself must NOT trip the breaker.
	slow2 := WithKubectlTimeout(slowRunner{delay: 60 * time.Millisecond}, "50ms")
	slow2.Run("kubectl", "delete", "pod", "y") // slow, errors, but is a mutation
	if _, err := slow2.Run("kubectl", "delete", "pod", "z"); err != nil && strings.Contains(err.Error(), "cluster unreachable") {
		t.Error("slow mutations must not trip the breaker")
	}
}
