package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exact netdev/drive shapes and complete unit parsing: every widening
// below passed the old substring/presence/first-match checks and is
// refused now. Each case names the element it widens.

// TestValidateCommand_NetdevShapeRefusals pins the admitted user-network
// shape: exactly `user` with id=net0 and one tcp SSH hostfwd. The old
// substring search admitted every row here as the valid forward.
func TestValidateCommand_NetdevShapeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(f *vfix)
		want   string
	}{
		{name: "udp neighbor alongside valid ssh", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=udp:0.0.0.0:4444-:4444,hostfwd=tcp:127.0.0.1:2201-:22")
		}, want: "ssh forward"},
		{name: "wrong netdev id", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net9,hostfwd=tcp:127.0.0.1:2201-:22")
		}, want: "ssh forward"},
		{name: "non-user netdev type", mutate: func(f *vfix) {
			f.setFlag("-netdev", "tap,id=net0,hostfwd=tcp:127.0.0.1:2201-:22")
		}, want: "ssh forward"},
		{name: "duplicate netdev key", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,id=net0,hostfwd=tcp:127.0.0.1:2201-:22")
		}, want: "ssh forward"},
		{name: "hostfwd without the dash", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:2201:22")
		}, want: "ssh forward"},
		{name: "guest forward to another port", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:2201-:23")
		}, want: "ssh forward"},
		{name: "guest forward with guest address", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:2201-10.0.2.15:80")
		}, want: "ssh forward"},
		{name: "duplicate hostfwd key", mutate: func(f *vfix) {
			f.setFlag("-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:2201-:22,hostfwd=tcp:127.0.0.1:2299-:22")
		}, want: "ssh forward"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVFix(t)
			tc.mutate(f)
			f.rewrite()
			err := ValidateCommand("gate")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want refusal containing %q", err, tc.want)
			}
		})
	}
}

// TestValidateCommand_DriveShapeRefusals pins exact drive option shapes
// with duplicate handling: the disk is exactly file/if/format, pflash
// code is exactly if/format/file with readonly=on, vars carries no
// readonly key at all.
func TestValidateCommand_DriveShapeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(f *vfix)
		want   string
	}{
		{name: "duplicate drive key", mutate: func(f *vfix) {
			disk := filepath.Join(f.dir, "disk.qcow2")
			f.setFlag("-drive", "file="+disk+",file="+disk+",if=virtio,format=qcow2")
		}, want: "duplicate"},
		{name: "readonly=off code drive", mutate: func(f *vfix) {
			for i := 0; i+1 < len(f.args); i++ {
				if f.args[i] == "-drive" && strings.Contains(f.args[i+1], "readonly=on") {
					f.args[i+1] = "if=pflash,format=raw,readonly=off,file=" + f.code
					return
				}
			}
			f.t.Fatal("pflash code drive not in argv")
		}, want: "readonly"},
		{name: "bare readonly code drive", mutate: func(f *vfix) {
			for i := 0; i+1 < len(f.args); i++ {
				if f.args[i] == "-drive" && strings.Contains(f.args[i+1], "readonly=on") {
					f.args[i+1] = "if=pflash,format=raw,readonly,file=" + f.code
					return
				}
			}
			f.t.Fatal("pflash code drive not in argv")
		}, want: "readonly"},
		{name: "vars drive with readonly key", mutate: func(f *vfix) {
			for i := 0; i+1 < len(f.args); i++ {
				if f.args[i] == "-drive" && strings.Contains(f.args[i+1], "vars.fd") {
					f.args[i+1] = f.args[i+1] + ",readonly=off"
					return
				}
			}
			f.t.Fatal("pflash vars drive not in argv")
		}, want: "readonly"},
		{name: "disk with extra option", mutate: func(f *vfix) {
			disk := filepath.Join(f.dir, "disk.qcow2")
			f.setFlag("-drive", "file="+disk+",if=virtio,format=qcow2,cache=writeback")
		}, want: "exactly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVFix(t)
			tc.mutate(f)
			f.rewrite()
			err := ValidateCommand("gate")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want refusal containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateCommand_SecondExecStartRefused(t *testing.T) {
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	unit := fmt.Sprintf("[Service]\nExecStart=%s %s\nExecStart=/bin/evil\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "ExecStart directives") {
		t.Errorf("second ExecStart must refuse naming the directives, got %v", err)
	}
}

func TestValidateCommand_ExecStartPreRefused(t *testing.T) {
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	unit := fmt.Sprintf("[Service]\nExecStartPre=/bin/evil\nExecStart=%s %s\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "ExecStartPre") {
		t.Errorf("ExecStartPre must refuse naming the directive, got %v", err)
	}
}

func TestValidateCommand_ExecStartPostRefused(t *testing.T) {
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	unit := fmt.Sprintf("[Service]\nExecStart=%s %s\nExecStartPost=/bin/evil\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "ExecStartPost") {
		t.Errorf("ExecStartPost must refuse naming the directive, got %v", err)
	}
}

func TestValidateCommand_EnvironmentRefused(t *testing.T) {
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	unit := fmt.Sprintf("[Service]\nEnvironment=LD_PRELOAD=/tmp/evil.so\nExecStart=%s %s\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "Environment") {
		t.Errorf("Environment must refuse naming the directive, got %v", err)
	}
}

func TestValidateCommand_MissingDropInPathsRefused(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	SetUserSystemd(func() bool { return true })
	t.Cleanup(func() { SetUserSystemd(nil) })
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "daemon-reload":
			return []byte{}, nil
		case "show":
			// FragmentPath present, DropInPaths MISSING: an
			// unverifiable load, never an empty one.
			return []byte("FragmentPath=" + unitPath + "\n"), nil
		default:
			return nil, fmt.Errorf("unexpected systemctl %v", args)
		}
	})
	t.Cleanup(func() { SetSystemctl(nil) })
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "cannot prove") {
		t.Errorf("missing DropInPaths must refuse naming the unverifiable load, got %v", err)
	}
}

// TestLoadedUnitFiles_DuplicateEvidenceRefused pins unique evidence:
// repeated property keys and repeated drop-in paths are malformed
// evidence (refuse), never values to last-win. Old code overwrote
// duplicates silently, admitting unverified content.
func TestLoadedUnitFiles_DuplicateEvidenceRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		show string
	}{
		{name: "duplicate DropInPaths key", show: "FragmentPath=/u\nDropInPaths=\nDropInPaths=/d\n"},
		{name: "duplicate FragmentPath key", show: "FragmentPath=/u\nFragmentPath=/u\nDropInPaths=\n"},
		{name: "duplicate drop-in path", show: "FragmentPath=/u\nDropInPaths=/d /d\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
				return []byte(tc.show), nil
			})
			t.Cleanup(func() { SetSystemctl(nil) })
			if _, _, ok := loadedUnitFiles("corral-gate"); ok {
				t.Errorf("%s must refuse (ok=false), got ok=true", tc.name)
			}
		})
	}
}

func TestValidateCommand_ExecConditionRefused(t *testing.T) {
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	unit := fmt.Sprintf("[Service]\nExecCondition=/bin/evil\nExecStart=%s %s\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "ExecCondition") {
		t.Errorf("ExecCondition must refuse naming the directive, got %v", err)
	}
}

// TestValidateCommand_LoadedExecStartMismatchRefused stages a loaded
// unit that actually executes something else: the on-disk unit matches
// the admitted command, but the manager loads a different fragment whose
// ExecStart differs. The refusal comes from fragment identity (the
// loaded execution definition is not the admitted one). Both old and new
// code refuse here — the test locks the loaded-mismatch negative across
// the evidence refactor.
func TestValidateCommand_LoadedExecStartMismatchRefused(t *testing.T) {
	f := newVFix(t)
	f.rewrite()
	other := filepath.Join(f.unitDir, "corral-other.service")
	otherUnit := "[Service]\nExecStart=/bin/evil --takeover\n"
	if err := os.WriteFile(other, []byte(otherUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	loadedUnitFixture(t, other, "", nil)
	if err := ValidateCommand("gate"); err == nil || !strings.Contains(err.Error(), "not the admitted unit") {
		t.Errorf("loaded ExecStart mismatch must refuse naming the fragment, got %v", err)
	}
}

func TestValidateCommand_IndentedExecStartCounts(t *testing.T) {
	f := newVFix(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	// Leading whitespace executes, so it counts: a single indented
	// admitted ExecStart validates (old code missed the line and
	// refused it as missing).
	unit := fmt.Sprintf("[Service]\n  ExecStart=%s %s\n", f.binary, strings.Join(f.args, " "))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand("gate"); err != nil {
		t.Errorf("single indented admitted ExecStart must validate, got %v", err)
	}
}
