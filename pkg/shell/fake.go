// Package shell provides a Runner interface for executing external commands,
// enabling unit tests to mock out kubectl, virtctl, and systemctl calls.
package shell

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
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

// Real is the production Runner that delegates to os/exec.
type Real struct{}

func (Real) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func (Real) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}

func (Real) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// Call records a single command invocation for test assertions.
type Call struct {
	Name  string
	Args  []string
	Stdin string // input passed via RunStdin ("" for plain Run)
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
	return f.recordAndRespond("", name, args)
}

// RunStdin records the call (stdin included) and returns the matching response.
func (f *Fake) RunStdin(stdin string, name string, args ...string) ([]byte, error) {
	return f.recordAndRespond(stdin, name, args)
}

func (f *Fake) recordAndRespond(stdin, name string, args []string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: name, Args: args, Stdin: stdin})
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
