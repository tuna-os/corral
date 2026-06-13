// Package doctor inspects a cluster for the pieces Corral's features need
// (KubeVirt, CDI, the right feature gates + LiveUpdate, snapshot/expand storage,
// the export proxy, metrics) and can reconcile the safe configuration fixes.
// Surfaced via `corral doctor`, the TUI, and the web UI.
package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanthor/corral/pkg/shell"
)

// Check is one diagnostic result.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Fixable bool   `json:"fixable"` // Corral can reconcile it (config-only, safe)
	fix     func() error
}

var runner shell.Runner = shell.Real{}

// SetRunner overrides the command runner (for unit tests).
func SetRunner(r shell.Runner) { runner = r }

func run(name string, args ...string) ([]byte, error) {
	return runner.Run(name, args...)
}

func ok(name string, args ...string) bool {
	_, err := runner.Run(name, args...)
	return err == nil
}

// Run executes all checks.
func Run() []Check {
	var checks []Check

	kvInstalled := ok("kubectl", "get", "kubevirt", "-n", "kubevirt")
	checks = append(checks, Check{
		Name:    "KubeVirt installed",
		OK:      kvInstalled,
		Detail:  detailIf(kvInstalled, "operator + CR present", "missing — fix installs the KubeVirt "+KubeVirtVersion+" operator + CR"),
		Fixable: !kvInstalled,
		fix:     installKubeVirt,
	})
	// Label selector (not hardcoded namespace) — CDI may be installed anywhere.
	cdiInstalled := ok("kubectl", "get", "deploy", "-A", "-l", "cdi.kubevirt.io=cdi-operator")
	checks = append(checks, Check{
		Name:    "CDI installed",
		OK:      cdiInstalled,
		Detail:  detailIf(cdiInstalled, "containerized-data-importer present", "missing — fix installs CDI "+CDIVersion+" (ISO/image imports)"),
		Fixable: !cdiInstalled,
		fix:     installCDI,
	})

	cfg := kubevirtConfig()
	rollout := cfg.RolloutStrategy == "LiveUpdate"
	checks = append(checks, Check{
		Name:    "LiveUpdate rollout strategy",
		OK:      rollout,
		Detail:  "lets CPU/RAM hotplug live-migrate; offline fallback otherwise",
		Fixable: kvInstalled && !rollout,
		fix:     reconcileKubeVirt,
	})
	liveMig := contains(cfg.WorkloadUpdateMethods, "LiveMigrate")
	checks = append(checks, Check{
		Name:    "LiveMigrate workload updates",
		OK:      liveMig,
		Detail:  "required by live CPU/RAM hotplug",
		Fixable: kvInstalled && !liveMig,
		fix:     reconcileKubeVirt,
	})
	for _, gate := range []string{"Snapshot", "HotplugVolumes", "VMExport"} {
		has := contains(cfg.FeatureGates, gate)
		checks = append(checks, Check{
			Name:    "Feature gate: " + gate,
			OK:      has,
			Detail:  gateDetail(gate),
			Fixable: kvInstalled && !has,
			fix:     reconcileKubeVirt,
		})
	}

	scs := storageClasses()
	hasDefault, hasExpand := false, false
	for _, sc := range scs {
		if sc.Default {
			hasDefault = true
		}
		if sc.Expand {
			hasExpand = true
		}
	}
	checks = append(checks, Check{
		Name:   "Default StorageClass",
		OK:     hasDefault,
		Detail: "needed to provision VM disks",
	})
	checks = append(checks, Check{
		Name:   "Expandable StorageClass",
		OK:     hasExpand,
		Detail: detailIf(hasExpand, "online disk expansion available", "no SC with allowVolumeExpansion — install Longhorn/CSI (setup guide)"),
	})
	hasSnap := hasSnapshotClass()
	checks = append(checks, Check{
		Name:   "VolumeSnapshotClass",
		OK:     hasSnap,
		Detail: detailIf(hasSnap, "persistent-VM snapshots available", "needs a CSI driver + external-snapshotter (setup guide)"),
	})

	expProxy := ok("kubectl", "get", "deploy", "-A", "-l", "kubevirt.io=virt-exportproxy")
	checks = append(checks, Check{
		Name:   "Export proxy",
		OK:     expProxy,
		Detail: "deployed by the VMExport gate — powers backup/export",
	})
	metrics := ok("kubectl", "get", "apiservices", "v1beta1.metrics.k8s.io")
	checks = append(checks, Check{
		Name:   "metrics-server",
		OK:     metrics,
		Detail: "live CPU/memory on the summary tab",
	})

	return checks
}

// FixOne reconciles a single named fixable check.
func FixOne(name string) error {
	for _, c := range Run() {
		if c.Name != name {
			continue
		}
		if c.OK {
			return nil
		}
		if !c.Fixable || c.fix == nil {
			return fmt.Errorf("%q is not auto-fixable", name)
		}
		return c.fix()
	}
	return fmt.Errorf("no check named %q", name)
}

// Fix reconciles every fixable check that isn't OK. Returns the names fixed.
func Fix() ([]string, error) {
	var fixed []string
	done := map[string]bool{} // dedupe shared fixes
	for _, c := range Run() {
		if c.OK || !c.Fixable || c.fix == nil {
			continue
		}
		key := fmt.Sprintf("%p", c.fix)
		if !done[key] {
			if err := c.fix(); err != nil {
				return fixed, err
			}
			done[key] = true
		}
		fixed = append(fixed, c.Name)
	}
	return fixed, nil
}

// ── Installation ──────────────────────────────────────────────────

// Pinned upstream versions the installer applies on a bare cluster.
const (
	KubeVirtVersion = "v1.8.3"
	CDIVersion      = "v1.65.0"
)

func applyURL(url string) error {
	out, err := run("kubectl", "apply", "-f", url)
	if err != nil {
		return fmt.Errorf("kubectl apply -f %s: %s", url, strings.TrimSpace(string(out)))
	}
	return nil
}

// installKubeVirt applies the upstream operator + CR, then best-effort
// reconciles Corral's feature gates (the CR may not be admitted yet — a
// follow-up `doctor fix` finishes the job once virt-operator is up).
func installKubeVirt() error {
	base := "https://github.com/kubevirt/kubevirt/releases/download/" + KubeVirtVersion
	if err := applyURL(base + "/kubevirt-operator.yaml"); err != nil {
		return err
	}
	if err := applyURL(base + "/kubevirt-cr.yaml"); err != nil {
		return err
	}
	reconcileKubeVirt() // best-effort; needs the admission webhook to be up
	return nil
}

// installCDI applies the upstream containerized-data-importer operator + CR.
func installCDI() error {
	base := "https://github.com/kubevirt/containerized-data-importer/releases/download/" + CDIVersion
	if err := applyURL(base + "/cdi-operator.yaml"); err != nil {
		return err
	}
	return applyURL(base + "/cdi-cr.yaml")
}

// ── KubeVirt config ───────────────────────────────────────────────

type kvConfig struct {
	RolloutStrategy       string
	WorkloadUpdateMethods []string
	FeatureGates          []string
}

func kubevirtConfig() kvConfig {
	out, err := run("kubectl", "get", "kubevirt", "kubevirt", "-n", "kubevirt", "-o", "json")
	if err != nil {
		return kvConfig{}
	}
	var kv struct {
		Spec struct {
			Configuration struct {
				VMRolloutStrategy      string `json:"vmRolloutStrategy"`
				DeveloperConfiguration struct {
					FeatureGates []string `json:"featureGates"`
				} `json:"developerConfiguration"`
			} `json:"configuration"`
			WorkloadUpdateStrategy struct {
				WorkloadUpdateMethods []string `json:"workloadUpdateMethods"`
			} `json:"workloadUpdateStrategy"`
		} `json:"spec"`
	}
	if json.Unmarshal(out, &kv) != nil {
		return kvConfig{}
	}
	return kvConfig{
		RolloutStrategy:       kv.Spec.Configuration.VMRolloutStrategy,
		WorkloadUpdateMethods: kv.Spec.WorkloadUpdateStrategy.WorkloadUpdateMethods,
		FeatureGates:          kv.Spec.Configuration.DeveloperConfiguration.FeatureGates,
	}
}

// reconcileKubeVirt enables LiveUpdate + LiveMigrate + the Snapshot/
// HotplugVolumes/VMExport gates, preserving any other gates already set.
func reconcileKubeVirt() error {
	cfg := kubevirtConfig()
	gates := union(cfg.FeatureGates, []string{"Snapshot", "HotplugVolumes", "VMExport"})
	methods := union(cfg.WorkloadUpdateMethods, []string{"LiveMigrate"})
	patch := map[string]any{
		"spec": map[string]any{
			"configuration": map[string]any{
				"vmRolloutStrategy":      "LiveUpdate",
				"developerConfiguration": map[string]any{"featureGates": gates},
			},
			"workloadUpdateStrategy": map[string]any{"workloadUpdateMethods": methods},
		},
	}
	body, _ := json.Marshal(patch)
	out, err := run("kubectl", "patch", "kubevirt", "kubevirt", "-n", "kubevirt",
		"--type", "merge", "-p", string(body))
	if err != nil {
		return fmt.Errorf("patching KubeVirt: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ── Storage helpers ───────────────────────────────────────────────

type scInfo struct {
	Default bool
	Expand  bool
}

func storageClasses() []scInfo {
	out, err := run("kubectl", "get", "sc", "-o", "json")
	if err != nil {
		return nil
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			AllowVolumeExpansion *bool `json:"allowVolumeExpansion"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil
	}
	var scs []scInfo
	for _, it := range res.Items {
		scs = append(scs, scInfo{
			Default: it.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true",
			Expand:  it.AllowVolumeExpansion != nil && *it.AllowVolumeExpansion,
		})
	}
	return scs
}

func hasSnapshotClass() bool {
	out, err := run("kubectl", "get", "volumesnapshotclass", "-o", "name")
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// ── small helpers ─────────────────────────────────────────────────

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	out := append([]string{}, a...)
	for _, x := range b {
		if !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func detailIf(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func gateDetail(gate string) string {
	switch gate {
	case "Snapshot":
		return "VM snapshots / restore"
	case "HotplugVolumes":
		return "add/remove disks on running VMs"
	case "VMExport":
		return "backup/export of VM disks"
	}
	return ""
}
