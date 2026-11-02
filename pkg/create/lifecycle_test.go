package create

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestBootcBackendSelection(t *testing.T) {
	for _, tt := range []struct{ explicit, kubevirt, reachable, want bool }{
		{false, false, true, true}, {false, true, false, false},
		{true, true, false, true}, {true, false, true, false},
	} {
		probes := 0
		svc := NewService(nil, nil)
		svc.Reachable = func() bool { probes++; return tt.reachable }
		r := request{Service: svc, Options: Options{KubevirtExplicit: tt.explicit, Kubevirt: tt.kubevirt}}
		if got := r.useKubevirtBootc(); got != tt.want {
			t.Errorf("%+v: got %v", tt, got)
		}
		if tt.explicit && probes != 0 {
			t.Error("explicit backend probed cluster")
		}
	}
}

func TestQEMULifecycle(t *testing.T) {
	sentinel := errors.New("lifecycle failed")
	for _, tt := range []struct {
		name              string
		start, wait       bool
		startErr, waitErr error
		want              []string
		err               error
	}{
		{name: "stopped"},
		{name: "start", start: true, want: []string{"start"}},
		{name: "wait implies start", wait: true, want: []string{"start", "wait"}},
		{name: "start failure", wait: true, startErr: sentinel, want: []string{"start"}, err: sentinel},
		{name: "wait failure", wait: true, waitErr: sentinel, want: []string{"start", "wait"}, err: sentinel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(nil, nil)
			var calls []string
			svc.QEMUStart = func(name string) error {
				if name != "vm" {
					t.Fatal(name)
				}
				calls = append(calls, "start")
				return tt.startErr
			}
			svc.QEMUWaitSSH = func(name, user string, timeout time.Duration) error {
				if name != "vm" || user != "tester" || timeout != 42*time.Second {
					t.Fatalf("probe: %s %s %s", name, user, timeout)
				}
				calls = append(calls, "wait")
				return tt.waitErr
			}
			r := request{Service: svc, Options: Options{Start: tt.start, WaitSSH: tt.wait, SSHUser: "tester", Timeout: 42}}
			if err := r.maybeStartAndWait("vm"); !errors.Is(err, tt.err) {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(calls, tt.want) {
				t.Fatalf("calls %v, want %v", calls, tt.want)
			}
		})
	}
}
