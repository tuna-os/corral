package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

func TestCopyEndpointParsing(t *testing.T) {
	for _, tc := range []struct {
		input, selector, path string
	}{
		{"file", "", "file"}, {"./local:name", "", "./local:name"},
		{"/tmp/local:name", "", "/tmp/local:name"},
		{"web:/tmp/a:b;$(touch nope) [x]*?", "web", "/tmp/a:b;$(touch nope) [x]*?"},
		{"web:relative name", "web", "relative name"},
		{"kubevirt/context:6443/guests/web:/tmp/a:b", "kubevirt/context:6443/guests/web", "/tmp/a:b"},
		{"peer/lab/kubevirt/context/guests/web:/tmp/file", "peer/lab/kubevirt/context/guests/web", "/tmp/file"},
	} {
		got, err := parseCopyEndpoint(tc.input)
		if err != nil || got.selector != tc.selector || got.path != tc.path {
			t.Errorf("parse %q = %+v, %v; want %q:%q", tc.input, got, err, tc.selector, tc.path)
		}
	}
	for _, invalid := range []string{"", ":/tmp/file", "web:", "web:/tmp/new\n!touch nope", "web:a\rbye", "file\x00name", "bad/selector/shape:/file"} {
		if _, err := parseCopyEndpoint(invalid); err == nil {
			t.Errorf("accepted malformed endpoint %q", invalid)
		}
	}
}

func TestCopyRefusesDirectionBackendAndPeersBeforeTransfer(t *testing.T) {
	for _, tc := range []struct {
		name, backend, source, destination, message string
	}{
		{"local pair", "kubevirt", "./one", "./two", "exactly one"},
		{"guest pair", "kubevirt", "web:/one", "other:/two", "exactly one"},
		{"malformed", "kubevirt", "./one", "web:", "<instance>:<path>"},
		{"exec is not copy", "incus", "./one", "web:/two", "incus backend does not support cp"},
		{"qemu", "qemu", "./one", "web:/two", "qemu backend does not support cp"},
		{"peer shorthand", "kubevirt", "./one", "lab/web:/two", "peer targets"},
		{"peer selector", "kubevirt", "./one", "peer/lab/kubevirt/prod/guests/web:/two", "peer targets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case lists "web" only on its own backend; a KubeVirt listing
			// beside it would make the bare name ambiguous.
			var fake *shell.Fake
			switch tc.backend {
			case "kubevirt":
				fake = kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "guests"})
			case "qemu":
				// Local QEMU has no context; the VM exists through its own state.
				fake = remoteVMScratch(t, "web", types.RegistryEntry{Backend: "qemu"})
				if err := qemu.RecordCreating("web", types.CreateOpts{}); err != nil {
					t.Fatal(err)
				}
			default:
				fake = remoteVMScratch(t, "web", types.RegistryEntry{Backend: tc.backend, Context: "prod"})
			}
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			command := newCopyCommand()
			err := command.RunE(command, []string{tc.source, tc.destination})
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("copy = %v; want %q", err, tc.message)
			}
			for _, call := range fake.Calls() {
				if filepath.Base(call.Name) == "sftp" || call.Stdin != "" {
					t.Fatalf("refused request performed a transfer: %+v", call)
				}
			}
			if _, err := os.Stat(filepath.Join(cache, "corral")); !os.IsNotExist(err) {
				t.Fatalf("refused request mutated trust cache: %v", err)
			}
		})
	}
}

func TestCopyRoutesBothDirectionsThroughKubeVirtCapability(t *testing.T) {
	for _, download := range []bool{false, true} {
		t.Run(map[bool]string{false: "upload", true: "download"}[download], func(t *testing.T) {
			fake := kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod %h ' $(false)", Namespace: "guests", Username: "ubuntu"})
			cache := t.TempDir()
			if err := os.Chmod(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_CACHE_HOME", cache)
			fake.AddPrefixResponse("/fake/bin/sftp", "", nil)
			command := newCopyCommand()
			if err := command.Flags().Set("recursive", "true"); err != nil {
				t.Fatal(err)
			}
			args := []string{"./local [x]*? ' \" $;:file", "web:guest [x]*? ' \" $;:file"}
			verb := "put -R "
			if download {
				args[0], args[1] = args[1], args[0]
				verb = "get -R "
			}
			if err := command.RunE(command, args); err != nil {
				t.Fatal(err)
			}
			var transfer *shell.Call
			for _, call := range fake.Calls() {
				if filepath.Base(call.Name) == "sftp" {
					copy := call
					transfer = &copy
				}
			}
			if transfer == nil || !strings.HasPrefix(transfer.Stdin, verb) || strings.Count(transfer.Stdin, "\n") != 1 {
				t.Fatalf("copy did not issue a single literal SFTP transfer: %+v", transfer)
			}
			options := strings.Join(transfer.Args, "\n")
			for _, want := range []string{"User=ubuntu", "'prod %%h '\"'\"' $(false)'", "'guests'", "'vmi/web'", "StrictHostKeyChecking=accept-new", "GlobalKnownHostsFile=/dev/null"} {
				if !strings.Contains(options, want) {
					t.Errorf("transport missing %q: %s", want, options)
				}
			}
		})
	}
}
