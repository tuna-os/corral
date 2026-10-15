package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
)

// withFlags saves and restores the package-level flag vars a test sets, so
// shuffled tests do not inherit each other's --format or --context.
func withFlags(t *testing.T) {
	t.Helper()
	saved := struct {
		ns, dest, src, size, ctx, format string
		keep                             bool
	}{flagNamespace, flagDest, flagSrc, flagSize, flagContext, flagFormat, flagKeepLocal}
	t.Cleanup(func() {
		flagNamespace, flagDest, flagSrc, flagSize = saved.ns, saved.dest, saved.src, saved.size
		flagContext, flagFormat, flagKeepLocal = saved.ctx, saved.format, saved.keep
	})
	flagNamespace, flagDest, flagSrc, flagSize, flagContext, flagFormat, flagKeepLocal = "", "", "", "", "", "", false
}

func withRunner(t *testing.T, r shell.Runner) {
	t.Helper()
	orig := runner
	runner = r
	t.Cleanup(func() { runner = orig })
}

// The remote file name is all a later restore has to go on, so its extension
// must name what the export really produced — including when the user left
// --format to the backend.
func TestBackupFileExtension_MatchesNativeFormat(t *testing.T) {
	withFlags(t)
	cases := []struct {
		adapter export.Adapter
		want    string
	}{
		{export.KubeVirt{}, "img.gz"},
		{export.QEMU{}, "qcow2"},
		{export.Libvirt{}, "qcow2"},
		{export.Incus{}, "tar.gz"},
		{export.PVE{}, "qcow2"},
	}
	for _, tc := range cases {
		f, err := resolveFormat(tc.adapter)
		if err != nil {
			t.Fatalf("%T: resolveFormat: %v", tc.adapter, err)
		}
		if f != tc.adapter.Formats()[0] {
			t.Errorf("%T: default format = %q, want the native %q", tc.adapter, f, tc.adapter.Formats()[0])
		}
		if got := extensionFor(f); got != tc.want {
			t.Errorf("%T: default backup is named .%s, want .%s", tc.adapter, got, tc.want)
		}
	}
}

func TestResolveFormat_Explicit(t *testing.T) {
	withFlags(t)
	flagFormat = "raw.gz"
	f, err := resolveFormat(export.QEMU{})
	if err != nil || f != export.RawGz {
		t.Fatalf("resolveFormat(raw.gz) = %q, %v; want raw.gz", f, err)
	}
	if extensionFor(f) != "img.gz" {
		t.Errorf("raw.gz backups are named .%s, want .img.gz", extensionFor(f))
	}

	flagFormat = "incus-tar"
	_, err = resolveFormat(export.QEMU{})
	if err == nil || !strings.Contains(err.Error(), `cannot produce "incus-tar"`) ||
		!strings.Contains(err.Error(), "qcow2, raw.gz") {
		t.Fatalf("unsupported format error = %v, want it to name the alternatives", err)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:               "0 B",
		1023:            "1023 B",
		1024:            "1.0 KiB",
		1536:            "1.5 KiB",
		5 << 20:         "5.0 MiB",
		20 << 30:        "20.0 GiB",
		3 << 40:         "3.0 TiB",
		(1 << 30) - 512: "1024.0 MiB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCronExpr_AllShorthands(t *testing.T) {
	for in, want := range map[string]string{"6h": "0 */6 * * *", "12h": "0 */12 * * *"} {
		if got, err := cronExpr(in); err != nil || got != want {
			t.Errorf("cronExpr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := cronExpr("* * * *"); err == nil {
		t.Error("a 4-field expression must be rejected")
	}
}

func writeConfig(t *testing.T, yaml string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CORRAL_DEFAULT_BACKEND", "")
	if err := os.MkdirAll(filepath.Join(dir, "corral"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corral", "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRef_ScopesByContext(t *testing.T) {
	withFlags(t)
	writeConfig(t, `contexts:
  - name: lab
    backend: kubevirt
    context: lab-kube
  - name: box
    backend: libvirt
    context: qemu:///system
`)

	flagContext = "lab"
	ref, err := backupRef("web")
	if err != nil {
		t.Fatalf("backupRef(lab): %v", err)
	}
	if ref.Backend != "kubevirt" || ref.Context != "lab-kube" || ref.Namespace != kubevirt.DefaultNamespace || ref.Name != "web" {
		t.Errorf("kubevirt ref = %+v, want lab-kube in the default namespace", ref)
	}

	flagNamespace = "team"
	if ref, _ := backupRef("web"); ref.Namespace != "team" {
		t.Errorf("-n team ignored: %+v", ref)
	}

	flagContext = "box"
	ref, err = backupRef("web")
	if err != nil {
		t.Fatalf("backupRef(box): %v", err)
	}
	if ref.Backend != "libvirt" || ref.Context != "qemu:///system" || ref.Namespace != "" {
		t.Errorf("libvirt ref = %+v, want no namespace (libvirt has none)", ref)
	}

	flagContext = "nope"
	if _, err := backupRef("web"); err == nil || !strings.Contains(err.Error(), `unknown context "nope"`) {
		t.Errorf("unknown context = %v, want a named error", err)
	}
}

func TestRunBackup_StopsBeforeExportWithoutRclone(t *testing.T) {
	withFlags(t)
	fake := shell.NewFake() // no rclone response: `rclone version` fails
	withRunner(t, fake)
	flagDest = "r2:backups"
	err := runBackup("web")
	if err == nil || !strings.Contains(err.Error(), "rclone not found") {
		t.Fatalf("runBackup without rclone = %v, want the install hint", err)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("made %d calls, want only the rclone probe: %+v", n, fake.Calls())
	}
}

func TestRunRestore_DownloadsThenUploadsIntoCDI(t *testing.T) {
	withFlags(t)
	fake := shell.NewFake()
	fake.AddResponseKV("rclone", []string{"version"}, "rclone v1.65", nil)
	fake.AddPrefixResponse("rclone copyto r2:backups/web-1.img.gz ", "", nil)
	fake.AddPrefixResponse("/fake/bin/virtctl image-upload", "uploaded", nil)
	withRunner(t, fake)
	kubevirt.SetDefaultRunner(fake)
	t.Cleanup(func() { kubevirt.SetDefaultRunner(nil) })

	flagSrc, flagSize, flagNamespace = "r2:backups/web-1.img.gz", "20Gi", "team"
	if err := runRestore("web-restored"); err != nil {
		t.Fatalf("runRestore: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want rclone version, copyto, virtctl", calls)
	}
	local := calls[1].Args[2]
	if calls[1].Name != "rclone" || calls[1].Args[0] != "copyto" || filepath.Base(local) != "web-1.img.gz" {
		t.Errorf("download call = %+v, want rclone copyto into a local web-1.img.gz", calls[1])
	}
	up := strings.Join(calls[2].Args, " ")
	for _, want := range []string{"image-upload dv web-restored", "--size 20Gi", "--image-path " + local, "--namespace team"} {
		if !strings.Contains(up, want) {
			t.Errorf("virtctl args %q missing %q", up, want)
		}
	}
	if _, err := os.Stat(filepath.Dir(local)); !os.IsNotExist(err) {
		t.Errorf("restore left its temp dir %s behind (stat err %v)", filepath.Dir(local), err)
	}
}

func TestRunRestore_DownloadFailureSkipsUpload(t *testing.T) {
	withFlags(t)
	fake := shell.NewFake()
	fake.AddResponseKV("rclone", []string{"version"}, "rclone v1.65", nil)
	fake.AddPrefixResponse("rclone copyto", "  directory not found  ", errors.New("exit 3"))
	withRunner(t, fake)
	kubevirt.SetDefaultRunner(fake)
	t.Cleanup(func() { kubevirt.SetDefaultRunner(nil) })

	flagSrc, flagSize = "r2:missing.img.gz", "10Gi"
	err := runRestore("disk")
	if err == nil || err.Error() != "rclone copyto: directory not found" {
		t.Fatalf("err = %v, want rclone's trimmed message", err)
	}
	for _, c := range fake.Calls() {
		if strings.HasSuffix(c.Name, "virtctl") {
			t.Errorf("uploaded into CDI after a failed download: %+v", c)
		}
	}
}

func TestUnscheduleCmd_DeletesCronJobThenSecret(t *testing.T) {
	withFlags(t)
	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl delete", "", nil)
	withRunner(t, fake)
	flagNamespace = "team"

	if err := unscheduleCmd().RunE(nil, []string{"web"}); err != nil {
		t.Fatalf("unschedule: %v", err)
	}
	calls := fake.Calls()
	want := []string{
		"delete cronjob corral-backup-web -n team --ignore-not-found",
		"delete secret corral-backup-rclone-web -n team --ignore-not-found",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %v", calls, want)
	}
	for i, c := range calls {
		if got := strings.Join(c.Args, " "); got != want[i] {
			t.Errorf("call %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestUnscheduleCmd_CronJobFailureKeepsSecret(t *testing.T) {
	withFlags(t)
	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl delete cronjob", "forbidden\n", errors.New("exit 1"))
	withRunner(t, fake)

	err := unscheduleCmd().RunE(nil, []string{"web"})
	if err == nil || err.Error() != "forbidden" {
		t.Fatalf("err = %v, want kubectl's message", err)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("made %d calls; the credentials Secret must outlive a CronJob that is still there", n)
	}
}

func TestSchedulesCmd_ListsByLabelInNamespace(t *testing.T) {
	withFlags(t)
	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl get cronjobs -n "+kubevirt.DefaultNamespace+" -l corral.dev/backupsched", "VM SCHEDULE\nweb 0 3 * * *\n", nil)
	withRunner(t, fake)
	if err := schedulesCmd().RunE(nil, nil); err != nil {
		t.Fatalf("schedules: %v", err)
	}
	fake2 := shell.NewFake()
	fake2.AddPrefixResponse("kubectl get cronjobs", "no cluster\n", errors.New("exit 1"))
	withRunner(t, fake2)
	if err := schedulesCmd().RunE(nil, nil); err == nil || err.Error() != "no cluster" {
		t.Errorf("err = %v, want kubectl's message", err)
	}
}
