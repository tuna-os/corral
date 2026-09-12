package vmtest

import (
	"strings"
	"testing"
	"time"
)

// The whole spec, in the shape a CI job would actually write it.
const fullSpec = `
name: gate
bootc: ghcr.io/tuna-os/yellowfin:latest
cpus: 4
memory: 8GiB
disk: 32GiB
timeout: 20m
sshUser: root
readyMarker: YELLOWFIN_READY
rootPassword: rootpw
users:
  - name: tester
    password: hunter2
    sudo: true
    groups: [video]
    shell: /bin/bash
    sshAuthorizedKeys:
      - ssh-ed25519 AAAAsomekey tester@example
packages:
  - jq
  - htop
extraRun:
  - dnf config-manager --set-enabled crb
files:
  - path: /etc/corral-test.conf
    content: |
      hello
    mode: "0600"
provision:
  - mode: image
    script: echo built
  - mode: system
    script: echo booted
checks:
  - systemctl is-active sshd
screenshots:
  interval: 3s
  video: true
  requirePaint: true
artifacts: out
keep: true
sudo: true
layerEngine: builtin
`

func TestParse_FullSpec(t *testing.T) {
	spec, err := Parse([]byte(fullSpec))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec.WithDefaults()
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if spec.Name != "gate" || spec.CPUs != 4 {
		t.Errorf("name/cpus = %q/%d", spec.Name, spec.CPUs)
	}
	// Lima writes 8GiB; qemu and bootc want 8G.
	if spec.Memory != "8G" || spec.Disk != "32G" {
		t.Errorf("memory/disk = %q/%q, want 8G/32G", spec.Memory, spec.Disk)
	}
	if spec.Timeout.Duration() != 20*time.Minute {
		t.Errorf("timeout = %s", spec.Timeout.Duration())
	}
	if spec.Screenshots.Interval.Duration() != 3*time.Second {
		t.Errorf("screenshot interval = %s", spec.Screenshots.Interval.Duration())
	}
	if len(spec.Users) != 1 || !spec.Users[0].Sudo || spec.Users[0].Password != "hunter2" {
		t.Errorf("users = %+v", spec.Users)
	}
	if got := spec.imageScripts(); len(got) != 1 || !strings.Contains(got[0], "built") {
		t.Errorf("image scripts = %v", got)
	}
	// Lima's "system" mode means "on the guest", so it is a boot script here.
	if got := spec.bootScripts(); len(got) != 1 || !strings.Contains(got[0], "booted") {
		t.Errorf("boot scripts = %v", got)
	}
	if !spec.needsLayer() {
		t.Error("a spec with users, packages and files needs a layer")
	}
}

func TestParse_RejectsUnknownFields(t *testing.T) {
	// A misspelled key that silently installs nothing wastes a whole CI run.
	_, err := Parse([]byte("bootc: x\npackage:\n  - jq\n"))
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
	if !strings.Contains(err.Error(), "package") {
		t.Errorf("error should name the offending field: %v", err)
	}
}

func TestWithDefaults(t *testing.T) {
	spec := &Spec{Bootc: "quay.io/fedora/fedora-bootc:41"}
	spec.WithDefaults()
	if spec.Name != DefaultName || spec.CPUs != DefaultCPUs || spec.Memory != DefaultMemory {
		t.Errorf("defaults not applied: %+v", spec)
	}
	if spec.Timeout.Duration() != DefaultTimeout || spec.SSHUser != DefaultSSHUser {
		t.Errorf("timeout/user = %s/%s", spec.Timeout.Duration(), spec.SSHUser)
	}
	if spec.Artifacts != DefaultArtifactDir || spec.LayerEngine != EngineAuto {
		t.Errorf("artifacts/engine = %s/%s", spec.Artifacts, spec.LayerEngine)
	}
	if spec.Screenshots.Interval.Duration() != DefaultFrameInterval {
		t.Errorf("interval = %s", spec.Screenshots.Interval.Duration())
	}
	if spec.needsLayer() {
		t.Error("a spec that asked for nothing must not build a layer")
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	spec := &Spec{
		ReadyMarker: "([unclosed",
		Users:       []User{{Name: ""}, {Name: "bad name"}},
		Files:       []File{{Path: "relative/path"}},
		LayerEngine: "podman",
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	for _, want := range []string{"no image to test", "readyMarker", "users[0]", "users[1]", "files[0]", "layerEngine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestNegativeIntervalTurnsCaptureOff(t *testing.T) {
	spec := &Spec{Bootc: "x", Screenshots: Screenshots{Interval: Duration(-1)}}
	spec.WithDefaults()
	if spec.Screenshots.Interval.Duration() > 0 {
		t.Errorf("a negative interval must stay off, got %s", spec.Screenshots.Interval.Duration())
	}
}

func TestDurationUnmarshal(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{in: "90s", want: 90 * time.Second},
		{in: "15m", want: 15 * time.Minute},
		{in: "600", want: 600 * time.Second},
		{in: "", want: 0},
		{in: "soon", bad: true},
	} {
		spec, err := Parse([]byte("bootc: x\ntimeout: \"" + tc.in + "\"\n"))
		if tc.bad {
			if err == nil {
				t.Errorf("timeout %q should not parse", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("timeout %q: %v", tc.in, err)
			continue
		}
		if spec.Timeout.Duration() != tc.want {
			t.Errorf("timeout %q = %s, want %s", tc.in, spec.Timeout.Duration(), tc.want)
		}
	}
}

func TestNormalizeSize(t *testing.T) {
	for in, want := range map[string]string{
		"8GiB":  "8G",
		"8G":    "8G",
		"4gb":   "4G",
		"2048":  "2048M",
		"":      "fallback",
		"lots":  "fallback",
		"32GiB": "32G",
	} {
		if got := normalizeSize(in, "fallback"); got != want {
			t.Errorf("normalizeSize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPasswordFor(t *testing.T) {
	spec := &Spec{RootPassword: "r", Users: []User{{Name: "tester", Password: "t"}}}
	if got := spec.passwordFor("root"); got != "r" {
		t.Errorf("root password = %q", got)
	}
	if got := spec.passwordFor("tester"); got != "t" {
		t.Errorf("tester password = %q", got)
	}
	if got := spec.passwordFor("nobody"); got != "" {
		t.Errorf("unknown user password = %q, want empty", got)
	}
}

func TestPasswordLoginWanted(t *testing.T) {
	if (&Spec{}).passwordLoginWanted() {
		t.Error("no passwords means no password logins")
	}
	if !(&Spec{RootPassword: "x"}).passwordLoginWanted() {
		t.Error("a root password wants password logins")
	}
	if !(&Spec{Users: []User{{Name: "a", Password: "x"}}}).passwordLoginWanted() {
		t.Error("a user password wants password logins")
	}
}

func TestReadyPattern(t *testing.T) {
	spec := &Spec{}
	if pattern, err := spec.readyPattern(); err != nil || pattern != nil {
		t.Errorf("no marker should give no pattern: %v, %v", pattern, err)
	}
	spec.ReadyMarker = "READY"
	pattern, err := spec.readyPattern()
	if err != nil {
		t.Fatalf("readyPattern: %v", err)
	}
	// Whole-line match, so the result can quote what it found.
	if got := pattern.FindString("[  4.11] guest: READY now\nmore"); got != "[  4.11] guest: READY now" {
		t.Errorf("matched %q", got)
	}
}

func TestNeedsLayer_EachTrigger(t *testing.T) {
	for name, spec := range map[string]*Spec{
		"users":     {Users: []User{{Name: "a"}}},
		"root pass": {RootPassword: "x"},
		"packages":  {Packages: []string{"jq"}},
		"extraRun":  {ExtraRun: []string{"true"}},
		"files":     {Files: []File{{Path: "/a"}}},
		"provision": {Provision: []Provision{{Script: "true"}}},
	} {
		if !spec.needsLayer() {
			t.Errorf("%s should need a layer", name)
		}
	}
}
