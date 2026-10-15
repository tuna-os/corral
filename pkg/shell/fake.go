// Package shell provides a Runner interface for executing external commands,
// enabling unit tests to mock out kubectl, virtctl, and systemctl calls.
package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/logging"
)

// Runner executes external commands and returns their output.
type Runner interface {
	// Run executes name with args and returns combined stdout+stderr.
	Run(name string, args ...string) ([]byte, error)

	// RunStdin is like Run but pipes stdin to the command.
	RunStdin(stdin string, name string, args ...string) ([]byte, error)

	// LookPath searches for an executable in PATH.
	LookPath(name string) (string, error)
}

// Real is the production Runner that delegates to os/exec. It logs every
// command at debug level, with secrets redacted. Bind gives it a request
// context, whose ID then appears on each of those records.
type Real struct {
	ctx context.Context
}

func (r Real) Run(name string, args ...string) ([]byte, error) {
	start := time.Now()
	out, err := exec.Command(name, args...).CombinedOutput()
	logCommand(r.ctx, name, args, 0, start, out, err)
	return out, err
}

func (r Real) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	start := time.Now()
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	logCommand(r.ctx, name, args, len(stdin), start, out, err)
	return out, err
}

func (r Real) bind(ctx context.Context) Runner { return Real{ctx: ctx} }

func (Real) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

type contextRunner struct {
	next    Runner
	context string
}

// WithKubeContext returns a runner permanently bound to one kubeconfig
// context. Unlike an environment variable, this is safe when the web server
// queries several clusters concurrently.
func WithKubeContext(r Runner, context string) Runner {
	if context == "" {
		return r
	}
	return &contextRunner{next: r, context: context}
}

func (r *contextRunner) args(name string, args []string) []string {
	base := filepath.Base(name)
	if base != "kubectl" && base != "virtctl" {
		return args
	}
	for _, arg := range args {
		if arg == "--context" || strings.HasPrefix(arg, "--context=") {
			return args
		}
	}
	return append([]string{"--context=" + r.context}, args...)
}
func (r *contextRunner) Run(name string, args ...string) ([]byte, error) {
	return r.next.Run(name, r.args(name, args)...)
}
func (r *contextRunner) RunStdin(stdin, name string, args ...string) ([]byte, error) {
	return r.next.RunStdin(stdin, name, r.args(name, args)...)
}
func (r *contextRunner) LookPath(name string) (string, error) { return r.next.LookPath(name) }
func (r *contextRunner) bind(ctx context.Context) Runner {
	return &contextRunner{next: Bind(ctx, r.next), context: r.context}
}

// Call records a single command invocation for test assertions.
type Call struct {
	Name      string
	Args      []string
	Stdin     string // input passed via RunStdin ("" for plain Run)
	RequestID string // request ID of the context the runner was bound to
}

// FakeResponse defines the canned response for a command match.
type FakeResponse struct {
	Stdout string
	Stderr string
	Err    error // if non-nil, Run returns this error (with Stdout still populated)
}

// Fake is a scriptable Runner for unit tests. Responses are matched by
// command string: "kubectl get vms -A -o json".
type Fake struct {
	mu              sync.Mutex
	responses       map[string]FakeResponse
	prefixResponses map[string]FakeResponse // matched by strings.HasPrefix
	calls           []Call
}

// NewFake creates a Fake with no responses.
func NewFake() *Fake {
	return &Fake{
		responses:       map[string]FakeResponse{},
		prefixResponses: map[string]FakeResponse{},
	}
}

// AddResponse registers a canned response for an exact command match.
// cmd should be the full command string, e.g. "kubectl get vms -A -o json".
func (f *Fake) AddResponse(cmd string, stdout string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[cmd] = FakeResponse{Stdout: stdout, Err: err}
}

// AddResponseKV is like AddResponse but takes name + args separately.
func (f *Fake) AddResponseKV(name string, args []string, stdout string, err error) {
	f.AddResponse(commandKey(name, args), stdout, err)
}

// AddPrefixResponse registers a canned response that matches any command
// starting with the given prefix. Used when exact args contain dynamic values
// (timestamps, JSON patches, generated names). Exact matches take priority.
func (f *Fake) AddPrefixResponse(prefix string, stdout string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefixResponses[prefix] = FakeResponse{Stdout: stdout, Err: err}
}

// Run records the call and returns the matching response. Returns an error
// if no response was registered for this command.
func (f *Fake) Run(name string, args ...string) ([]byte, error) {
	return f.recordAndRespond("", "", name, args)
}

// RunStdin records the call (stdin included) and returns the matching response.
func (f *Fake) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	return f.recordAndRespond("", stdin, name, args)
}

// bind returns a view of f that records the request ID of ctx on each Call,
// so tests can assert that a request's context reached its commands.
func (f *Fake) bind(ctx context.Context) Runner {
	return boundFake{fake: f, requestID: logging.RequestID(ctx)}
}

type boundFake struct {
	fake      *Fake
	requestID string
}

func (b boundFake) Run(name string, args ...string) ([]byte, error) {
	return b.fake.recordAndRespond(b.requestID, "", name, args)
}
func (b boundFake) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	return b.fake.recordAndRespond(b.requestID, stdin, name, args)
}
func (b boundFake) LookPath(name string) (string, error) { return b.fake.LookPath(name) }
func (b boundFake) bind(ctx context.Context) Runner      { return b.fake.bind(ctx) }

func (f *Fake) recordAndRespond(requestID, stdin, name string, args []string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: name, Args: args, Stdin: stdin, RequestID: requestID})
	f.mu.Unlock()

	key := commandKey(name, args)

	// 1. Exact match
	f.mu.Lock()
	r, ok := f.responses[key]
	f.mu.Unlock()
	if ok {
		return []byte(r.Stdout + r.Stderr), r.Err
	}

	// 2. Prefix match (longest prefix wins)
	f.mu.Lock()
	var bestPrefix string
	for p := range f.prefixResponses {
		if strings.HasPrefix(key, p) && len(p) > len(bestPrefix) {
			bestPrefix = p
		}
	}
	r, ok = f.prefixResponses[bestPrefix]
	f.mu.Unlock()
	if ok {
		return []byte(r.Stdout + r.Stderr), r.Err
	}

	return nil, fmt.Errorf("fake: no response registered for %q — add with AddResponse() or AddPrefixResponse()", key)
}

// LookPath always returns a fake path.
func (f *Fake) LookPath(name string) (string, error) {
	return "/fake/bin/" + name, nil
}

// Calls returns all recorded invocations in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// LastCall returns the most recent invocation, or nil if none.
func (f *Fake) LastCall() *Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	c := f.calls[len(f.calls)-1]
	return &c
}

// Reset clears all recorded calls and responses.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.responses = map[string]FakeResponse{}
	f.prefixResponses = map[string]FakeResponse{}
}

func commandKey(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

// WithKubectlTimeout wraps a Runner so every kubectl invocation carries a
// client-side --request-timeout (unless the caller set one), plus a circuit
// breaker: once a kubectl call times out, further kubectl calls fail
// instantly for a short window instead of each burning its own timeout.
// Without this, a kubeconfig pointing at a dead cluster — asleep homelab,
// VPN down, stale context — hangs every corral surface for minutes (one
// list stacks 4–5 sequential kubectl calls). Wrap only production runners:
// test Fakes match exact argument lists.
func WithKubectlTimeout(r Runner, timeout string) Runner {
	d, _ := time.ParseDuration(timeout)
	return &kubectlTimeoutRunner{next: r, timeout: timeout, timeoutDur: d, breaker: &breaker{}}
}

// DefaultKubectl is the shared production kubectl runner: Real wrapped with
// the timeout + circuit breaker. A single instance on purpose — the breaker
// only works if every package's kubectl calls share its state, so one timed-out
// call anywhere makes the whole binary fail fast until the TTL expires.
// CORRAL_KUBECTL_TIMEOUT overrides the per-call budget (e.g. "30s" for a
// slow control plane).
var DefaultKubectl Runner = WithKubectlTimeout(Real{}, kubectlTimeoutDefault())

// Command returns a streaming command with Corral's selected Kubernetes
// context injected. Long-lived operations (console, exec, logs -f) cannot use
// Runner because they need direct stdin/stdout access, but they must still
// obey the same context contract as ordinary kubectl calls.
func Command(name string, args ...string) *exec.Cmd {
	if base := filepath.Base(name); base == "kubectl" || base == "virtctl" {
		args = injectKubectlContext(args)
	}
	return exec.Command(name, args...)
}

func CommandForContext(context, name string, args ...string) *exec.Cmd {
	if context != "" {
		has := false
		for _, arg := range args {
			if arg == "--context" || strings.HasPrefix(arg, "--context=") {
				has = true
			}
		}
		if !has && (filepath.Base(name) == "kubectl" || filepath.Base(name) == "virtctl") {
			args = append([]string{"--context=" + context}, args...)
		}
	}
	return exec.Command(name, args...)
}

func kubectlTimeoutDefault() string {
	if v := os.Getenv("CORRAL_KUBECTL_TIMEOUT"); v != "" {
		if _, err := time.ParseDuration(v); err == nil {
			return v
		}
	}
	return "5s"
}

// breakerTTL is how long kubectl calls short-circuit after a timeout. Short
// on purpose: a cluster coming back is noticed within seconds.
const breakerTTL = 15 * time.Second

type kubectlTimeoutRunner struct {
	next       Runner
	timeout    string
	timeoutDur time.Duration

	*breaker
}

// breaker is the circuit-breaker state. A pointer, so that every runner
// Bind derives from DefaultKubectl trips and clears the same breaker.
type breaker struct {
	mu        sync.Mutex
	deadUntil time.Time
}

func (k *kubectlTimeoutRunner) bind(ctx context.Context) Runner {
	bound := *k
	bound.next = Bind(ctx, k.next)
	return &bound
}

func (k *kubectlTimeoutRunner) inject(name string, args []string) []string {
	args = injectKubectlContext(args)
	for _, a := range args {
		if strings.HasPrefix(a, "--request-timeout") {
			return args
		}
	}
	return append([]string{"--request-timeout=" + k.timeout}, args...)
}

func injectKubectlContext(args []string) []string {
	if context := os.Getenv("CORRAL_KUBE_CONTEXT"); context != "" {
		hasContext := false
		for _, a := range args {
			if a == "--context" || strings.HasPrefix(a, "--context=") {
				hasContext = true
			}
		}
		if !hasContext {
			args = append([]string{"--context=" + context}, args...)
		}
	}
	return args
}

// call wraps one kubectl execution with the breaker bookkeeping.
func (k *kubectlTimeoutRunner) call(fn func() ([]byte, error)) ([]byte, error) {
	k.mu.Lock()
	if time.Now().Before(k.deadUntil) {
		until := time.Until(k.deadUntil).Round(time.Second)
		k.mu.Unlock()
		return nil, fmt.Errorf("cluster unreachable (a recent kubectl call timed out; retrying in %s)", until)
	}
	k.mu.Unlock()

	start := time.Now()
	out, err := fn()
	if err != nil && k.looksLikeTimeout(out, time.Since(start)) {
		k.mu.Lock()
		k.deadUntil = time.Now().Add(breakerTTL)
		k.mu.Unlock()
	} else if err == nil {
		k.mu.Lock()
		k.deadUntil = time.Time{}
		k.mu.Unlock()
	}
	return out, err
}

// looksLikeTimeout classifies a failed kubectl call as "cluster unreachable"
// — either it burned most of its request timeout, or kubectl said so. kubectl
// often exits marginally before the parsed duration, so a strict >= check
// never fires; 80%% is the tripwire.
func (k *kubectlTimeoutRunner) looksLikeTimeout(out []byte, elapsed time.Duration) bool {
	if k.timeoutDur > 0 && elapsed >= k.timeoutDur*8/10 {
		return true
	}
	for _, marker := range []string{
		"context deadline exceeded", "Client.Timeout", "Unable to connect to the server",
		"connection refused", "no route to host", "i/o timeout",
	} {
		if strings.Contains(string(out), marker) {
			return true
		}
	}
	return false
}

// readOnlyVerb reports whether a kubectl invocation is a fast read — the
// only class that gets the request-timeout flag and the hard process
// deadline. Mutations (delete waits on graceful pod termination, apply on
// server round-trips, exec on the command itself) legitimately exceed a
// read budget — the first kind-CI run of the CT e2e proved it: the deadline
// killed a normal `kubectl delete pod` mid-termination. They still consult
// the breaker, so a dead cluster fails them fast once any read has tripped.
func readOnlyVerb(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		switch a {
		case "get", "top", "version", "api-resources", "api-versions", "auth", "explain":
			return true
		}
		return false
	}
	return false
}

// breakerOnly gates a call on the tripped breaker without arming it — used
// for non-read kubectl verbs, whose slowness is not evidence of a dead
// cluster.
func (k *kubectlTimeoutRunner) breakerOnly(fn func() ([]byte, error)) ([]byte, error) {
	k.mu.Lock()
	if time.Now().Before(k.deadUntil) {
		until := time.Until(k.deadUntil).Round(time.Second)
		k.mu.Unlock()
		return nil, fmt.Errorf("cluster unreachable (a recent kubectl call timed out; retrying in %s)", until)
	}
	k.mu.Unlock()
	return fn()
}

func (k *kubectlTimeoutRunner) Run(name string, args ...string) ([]byte, error) {
	if filepath.Base(name) != "kubectl" {
		return k.next.Run(name, args...)
	}
	if !readOnlyVerb(args) {
		return k.breakerOnly(func() ([]byte, error) { return k.next.Run(name, injectKubectlContext(args)...) })
	}
	if _, isReal := k.next.(Real); isReal {
		// --request-timeout caps each HTTP request, but kubectl's discovery
		// phase retries several endpoints sequentially against a dead API —
		// one invocation can burn 5× the flag. A hard process deadline is the
		// only real bound.
		return k.call(func() ([]byte, error) { return k.hardRun("", name, k.inject(name, args)) })
	}
	return k.call(func() ([]byte, error) { return k.next.Run(name, k.inject(name, args)...) })
}

func (k *kubectlTimeoutRunner) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	if filepath.Base(name) != "kubectl" {
		return k.next.RunStdin(stdin, name, args...)
	}
	if !readOnlyVerb(args) {
		return k.breakerOnly(func() ([]byte, error) { return k.next.RunStdin(stdin, name, injectKubectlContext(args)...) })
	}
	if _, isReal := k.next.(Real); isReal {
		return k.call(func() ([]byte, error) { return k.hardRun(stdin, name, k.inject(name, args)) })
	}
	return k.call(func() ([]byte, error) { return k.next.RunStdin(stdin, name, k.inject(name, args)...) })
}

// hardRun executes kubectl with a hard process deadline (the request timeout
// plus grace), killing it outright when the deadline passes.
func (k *kubectlTimeoutRunner) hardRun(stdin, name string, args []string) ([]byte, error) {
	deadline := k.timeoutDur + 3*time.Second
	if k.timeoutDur <= 0 {
		deadline = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = fmt.Errorf("kubectl killed after %s: context deadline exceeded", deadline)
	}
	r, _ := k.next.(Real) // hardRun is only reached with a Real next
	logCommand(r.ctx, name, args, len(stdin), start, out, err)
	return out, err
}

func (k *kubectlTimeoutRunner) LookPath(name string) (string, error) {
	return k.next.LookPath(name)
}
