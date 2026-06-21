package kubevirt

import "testing"

func TestParseMilliCPU(t *testing.T) {
	cases := map[string]int{
		"123m": 123,
		"0m":   0,
		"1":    1000, // one full core
		"2":    2000,
		"":     0,
		"bad":  0,
	}
	for in, want := range cases {
		if got := parseMilliCPU(in); got != want {
			t.Errorf("parseMilliCPU(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSampleAllCPU(t *testing.T) {
	_, r := newFakeClient()
	// Launcher pods → vm names via the vm.kubevirt.io/name label.
	r.AddResponseKV("kubectl", []string{"get", "pods", "-A", "-l", "kubevirt.io=virt-launcher", "-o", "json"},
		`{"items":[
			{"metadata":{"name":"virt-launcher-web-abcde","namespace":"corral-vms","labels":{"vm.kubevirt.io/name":"web"}}},
			{"metadata":{"name":"virt-launcher-db-fghij","namespace":"corral-vms","labels":{"vm.kubevirt.io/name":"db"}}}
		]}`, nil)
	// `kubectl top` rows: NAMESPACE NAME CPU MEM.
	r.AddResponseKV("kubectl", []string{"top", "pod", "-A", "-l", "kubevirt.io=virt-launcher", "--no-headers"},
		"corral-vms   virt-launcher-web-abcde   250m   512Mi\ncorral-vms   virt-launcher-db-fghij   1   1024Mi", nil)

	got := SampleAllCPU()
	if got["corral-vms/web"] != 250 {
		t.Errorf("web CPU = %d, want 250", got["corral-vms/web"])
	}
	if got["corral-vms/db"] != 1000 {
		t.Errorf("db CPU = %d, want 1000 (1 core)", got["corral-vms/db"])
	}
}

func TestSampleAllCPU_NoMetricsServer(t *testing.T) {
	_, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "pods", "-A", "-l", "kubevirt.io=virt-launcher", "-o", "json"},
		`{"items":[{"metadata":{"name":"virt-launcher-web-x","namespace":"ns","labels":{"vm.kubevirt.io/name":"web"}}}]}`, nil)
	// `kubectl top` errors when metrics-server is absent → nil (degrade).
	r.AddResponseKV("kubectl", []string{"top", "pod", "-A", "-l", "kubevirt.io=virt-launcher", "--no-headers"},
		"", errSimulated)

	if got := SampleAllCPU(); got != nil {
		t.Errorf("expected nil when metrics-server absent, got %v", got)
	}
}
