package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// importedPkgTrees returns the top-level pkg/ trees (for example "vdi" for
// pkg/vdi/...) that target imports, as `go list -deps` reports them with env
// added to the environment.
func importedPkgTrees(t *testing.T, env []string, target string, flags ...string) map[string]bool {
	t.Helper()
	args := append(append([]string{"list"}, flags...), "-deps", "-f", "{{.ImportPath}}", target)
	cmd := exec.Command("go", args...)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	const prefix = "github.com/tuna-os/corral/pkg/"
	trees := map[string]bool{}
	for _, p := range strings.Fields(string(out)) {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			trees[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	if len(trees) == 0 {
		t.Fatalf("go %s: no %s packages; the module path changed?", strings.Join(args, " "), prefix)
	}
	return trees
}

// .github/scripts/ci-scope.sh skips the e2e lane for changes under pkg/ trees
// that the corral binary does not import. That list is copied by hand from
// `go list -tags bootc -deps .`. A tree that the binary starts to import must
// leave the list, or a change there no longer runs e2e-kind. The import graph
// changes only in .go files, which select the test lane, so this runs on the
// PRs that can cause the drift.
func TestCIScopeE2ESkipsOnlyUnimportedTrees(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "scripts", "ci-scope.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// The e2e drop case is the one pattern list that names the plugin binaries.
	var drop string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "cmd/corral-*/*") && strings.HasSuffix(strings.TrimSpace(line), ") ;;") {
			drop = line
			break
		}
	}
	if drop == "" {
		t.Fatal("ci-scope.sh: e2e drop case (the line naming cmd/corral-*/*) not found; update this test")
	}
	m := regexp.MustCompile(`pkg/([a-z0-9_-]+)/\*`).FindAllStringSubmatch(drop, -1)
	if len(m) == 0 {
		t.Fatalf("ci-scope.sh: no pkg/<tree>/* patterns in the e2e drop case:\n%s", drop)
	}
	imported := importedPkgTrees(t, nil, ".", "-tags", "bootc")
	for _, sub := range m {
		if tree := sub[1]; imported[tree] {
			t.Errorf("ci-scope.sh skips e2e for pkg/%s/*, but the corral binary imports pkg/%s: drop it from the e2e case", tree, tree)
		}
	}
}
