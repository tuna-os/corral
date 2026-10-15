package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/vdi"
)

func TestRootCmd_HasCTPool(t *testing.T) {
	root := rootCmd()
	c, _, err := root.Find([]string{"ct-pool"})
	if err != nil || c == root {
		t.Fatal("missing subcommand ct-pool")
	}
	for _, want := range []string{"apply", "reconcile", "status", "list", "delete", "claim", "release"} {
		sub, _, err := c.Find([]string{want})
		if err != nil || sub == c {
			t.Errorf("ct-pool: missing subcommand %q", want)
		}
	}
}

func TestCTPoolApplyCmd_PersistentModeFailsBeforeTouchingTheCluster(t *testing.T) {
	fake := shell.NewFake() // no responses: any kubectl call fails the test
	vdi.SetRunner(fake)
	defer vdi.SetRunner(shell.Real{})

	c := ctPoolApplyCmd()
	_ = c.Flags().Set("image", "ghcr.io/example/desktop:1")
	_ = c.Flags().Set("mode", vdi.CTPoolModePersistent)
	err := c.RunE(c, []string{"desk"})
	if !errors.Is(err, vdi.ErrPersistentCTPool) {
		t.Fatalf("apply --mode persistent: %v", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Fatalf("a rejected pool reached the cluster: %+v", calls)
	}
}

func TestCTPoolStatusCmd_ReadsLiveState(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"get", "configmap", "corral-vdi-ctpool-desk", "-n", "vdi", "--ignore-not-found", "-o", "json"},
		`{"metadata":{"name":"corral-vdi-ctpool-desk","namespace":"vdi","labels":{"corral.dev/vdi-ct-pool":"desk"}},
		  "data":{"spec":"{\"name\":\"desk\",\"namespace\":\"vdi\",\"mode\":\"ephemeral\",\"size\":2,\"source\":{\"image\":\"img:1\"}}"}}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "pvc", "-n", "vdi", "-l", "corral.dev/vdi-ct-pool=desk", "-o", "json"},
		`{"items":[{"metadata":{"name":"desk-1-data","labels":{"corral.dev/vdi-ct-member":"desk-1"}}}]}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "pods", "-n", "vdi", "-l", "corral.dev/vdi-ct-pool=desk", "-o", "json"}, `{"items":[]}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "-n", "vdi", "-l", "corral.dev/vdi-ct-pool=desk", "-o", "json"}, `{"items":[]}`, nil)
	vdi.SetRunner(fake)
	defer vdi.SetRunner(shell.Real{})
	namespace = "vdi"
	defer func() { namespace = "" }()

	c := ctPoolStatusCmd()
	if err := c.RunE(c, []string{"desk"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, call := range fake.Calls() {
		if verb := call.Args[0]; verb != "get" {
			t.Fatalf("status must be read-only, ran kubectl %s", strings.Join(call.Args, " "))
		}
	}
}

func TestCTPoolReleaseCmd_Args(t *testing.T) {
	c := ctPoolReleaseCmd()
	if err := c.Args(c, []string{"desk"}); err == nil {
		t.Error("release needs a pool and a member")
	}
	if err := c.Args(c, []string{"desk", "desk-1", "alice", "extra"}); err == nil {
		t.Error("release takes at most pool, member, and identity")
	}
}
