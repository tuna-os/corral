package kubevirt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

func copyTrustRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCopyBatchUsesLiteralPathsAndExplicitDirection(t *testing.T) {
	local, err := filepath.Abs("file [x]*? ' \" \\ $();: %h")
	if err != nil {
		t.Fatal(err)
	}
	for _, fromGuest := range []bool{false, true} {
		for _, recursive := range []bool{false, true} {
			opts := types.CopyOptions{LocalPath: local, GuestPath: "-guest [x]*? ' \" \\ $();: %h", FromGuest: fromGuest, Recursive: recursive}
			batch, err := copyBatch(opts)
			if err != nil {
				t.Fatal(err)
			}
			verb, source, destination := "put", local, "./"+opts.GuestPath
			if fromGuest {
				verb, source, destination = "get", destination, source
			}
			if recursive {
				verb += " -R"
			}
			want := verb + " " + sftpQuote(source) + " " + sftpQuote(destination) + "\n"
			if batch != want {
				t.Errorf("batch = %q; want %q", batch, want)
			}
			if strings.Contains(batch, `\*`) || strings.Contains(batch, `\[`) || strings.Count(batch, "\n") != 1 {
				t.Errorf("batch introduced glob double-escaping or an extra command: %q", batch)
			}
		}
	}
}

func TestCopyRejectsMalformedPathsBeforeToolsOrTrust(t *testing.T) {
	for _, path := range []string{"", "line\n!touch nope", "line\rbye", "a\x00b"} {
		for _, fromGuest := range []bool{false, true} {
			fake := shell.NewFake()
			c := NewClientWithRunner("guests", fake)
			opts := types.CopyOptions{LocalPath: "local", GuestPath: path, FromGuest: fromGuest, User: "ubuntu"}
			if err := c.Copy("web", opts); err == nil {
				t.Errorf("accepted invalid guest path %q", path)
			}
			opts.LocalPath, opts.GuestPath = path, "guest"
			if err := c.Copy("web", opts); err == nil {
				t.Errorf("accepted invalid local path %q", path)
			}
			if len(fake.Calls()) != 0 {
				t.Fatalf("invalid request reached a tool: %+v", fake.Calls())
			}
		}
	}
}

func TestCopyResolvesAmbientContextBeforeHostKeyTrustAndPropagatesFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", copyTrustRoot(t))
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	fake := shell.NewFake()
	fake.AddResponse("kubectl config current-context", "actual-context\n", nil)
	fake.AddPrefixResponse("/fake/bin/sftp", "host key verification failed", errors.New("exit status 255"))
	c := NewClientWithRunner("guests", fake)
	err := c.Copy("web", types.CopyOptions{LocalPath: "local", GuestPath: "/tmp/file", User: "ubuntu"})
	if err == nil || !strings.Contains(err.Error(), "host key verification failed") {
		t.Fatalf("lost transfer/trust refusal: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Name != "kubectl" || !strings.Contains(strings.Join(calls[1].Args, " "), "'actual-context'") {
		t.Fatalf("ambient context was not frozen into the tunnel: %+v", calls)
	}
}
