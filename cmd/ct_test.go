package cmd

import (
	"testing"

	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/shell"
)

func TestCtCmd_Subcommands(t *testing.T) {
	for _, want := range []string{"create", "list", "start", "stop", "delete", "console"} {
		c, _, err := ctCmd.Find([]string{want})
		if err != nil || c == ctCmd {
			t.Errorf("ct: missing subcommand %q", want)
		}
	}
}

func TestCtCreateCmd_RequiresImage(t *testing.T) {
	ctImage = ""
	err := ctCreateCmd.RunE(ctCreateCmd, []string{"web1"})
	if err == nil {
		t.Error("expected an error when --image is missing")
	}
}

func TestCtCreateCmd_Flags(t *testing.T) {
	for _, name := range []string{"image", "cpu", "mem", "disk", "storage-class", "privileged"} {
		if ctCreateCmd.Flags().Lookup(name) == nil {
			t.Errorf("ct create: missing --%s flag", name)
		}
	}
}

func TestCtCreateCmd_Success(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	ct.SetRunner(fake)
	defer ct.SetRunner(shell.Real{})

	ctImage = "debian:bookworm"
	ctNamespace = "corral-ct"
	ctCPU = 1
	ctMem = "512Mi"
	ctDisk = "5Gi"
	defer func() { ctImage, ctNamespace = "", "" }()

	if err := ctCreateCmd.RunE(ctCreateCmd, []string{"web1"}); err != nil {
		t.Fatalf("ct create: %v", err)
	}
	applies := 0
	for _, c := range fake.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	if applies != 2 {
		t.Errorf("expected 2 applies (PVC + pod), got %d", applies)
	}
}

func TestCtListCmd_Empty(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"get", "pvc", "-A", "-l", "corral.dev/ct=true", "-o", "json"}, `{"items":[]}`, nil)
	ct.SetRunner(fake)
	defer ct.SetRunner(shell.Real{})

	if err := ctListCmd.RunE(ctListCmd, nil); err != nil {
		t.Fatalf("ct list: %v", err)
	}
}

func TestCtNamespaceOrDefault(t *testing.T) {
	ctNamespace = "custom-ns"
	if got := ctNamespaceOrDefault(); got != "custom-ns" {
		t.Errorf("got %q, want custom-ns", got)
	}
	ctNamespace = ""
	if got := ctNamespaceOrDefault(); got == "" {
		t.Error("expected a non-empty default namespace")
	}
}
