package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

// fakeEC2 is an EC2 Query API stand-in. Each region answers DescribeInstances
// with its own XML and records every form it receives, so a test can assert
// which actions ran against which instance.
type fakeEC2 struct {
	mu       sync.Mutex
	describe map[string]string // region → DescribeInstances body
	fail     map[string]int    // action → HTTP status to fail with
	calls    []url.Values
	regions  []string
	auth     []string
}

func newFakeEC2(t *testing.T) *fakeEC2 {
	t.Helper()
	f := &fakeEC2{describe: map[string]string{}, fail: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		region := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.calls = append(f.calls, form)
		f.regions = append(f.regions, region)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		status := f.fail[form.Get("Action")]
		desc := f.describe[region]
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>not allowed</Message></Error></Errors></Response>`))
			return
		}
		if form.Get("Action") == "DescribeInstances" {
			_, _ = w.Write([]byte(desc))
			return
		}
		_, _ = w.Write([]byte(`<ok/>`))
	}))
	t.Cleanup(srv.Close)
	orig := endpoint
	endpoint = func(region string) string { return srv.URL + "/" + region }
	t.Cleanup(func() { endpoint = orig })
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("CORRAL_AWS_POWER_REGIONS", "")
	return f
}

func instanceXML(id, state, name string) string {
	return `<DescribeInstancesResponse><reservationSet><item><instancesSet><item>` +
		`<instanceId>` + id + `</instanceId><instanceType>t3.large</instanceType>` +
		`<instanceState><name>` + state + `</name></instanceState>` +
		`<tagSet><item><key>corral:host-power</key><value>` + name + `</value></item></tagSet>` +
		`</item></instancesSet></item></reservationSet></DescribeInstancesResponse>`
}

func TestRun_ListMergesRegionsSortedByName(t *testing.T) {
	f := newFakeEC2(t)
	f.describe["eu-north-1"] = instanceXML("i-0b", "running", "zulu")
	f.describe["us-east-1"] = instanceXML("i-0a", "stopped", "alpha")
	t.Setenv("CORRAL_AWS_POWER_REGIONS", " eu-north-1, ,us-east-1 ")

	var out bytes.Buffer
	if err := run([]string{"host-power", "list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	var hosts []sdk.Host
	if err := json.Unmarshal(out.Bytes(), &hosts); err != nil {
		t.Fatalf("list output is not JSON hosts: %v\n%s", err, out.String())
	}
	if len(hosts) != 2 || hosts[0].ID != "us-east-1/i-0a" || hosts[1].ID != "eu-north-1/i-0b" {
		t.Fatalf("hosts = %+v, want alpha (us-east-1) then zulu (eu-north-1)", hosts)
	}
	// The opt-in tag is the filter: DescribeInstances must ask for it, or the
	// plugin would offer to power every instance in the account.
	for _, form := range f.calls {
		if form.Get("Action") != "DescribeInstances" || form.Get("Filter.1.Name") != "tag-key" || form.Get("Filter.1.Value.1") != tagOptIn {
			t.Errorf("describe call not filtered on the opt-in tag: %v", form)
		}
	}
	for _, a := range f.auth {
		if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AKIDTEST/") {
			t.Errorf("request not SigV4-signed with the env key: %q", a)
		}
	}
}

func TestRun_ListWithNoRegionsIsEmptyJSONArray(t *testing.T) {
	f := newFakeEC2(t)
	var out bytes.Buffer
	if err := run([]string{"host-power", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	// Corral parses this output; "null" would not be a host list.
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("output = %q, want []", out.String())
	}
	if len(f.calls) != 0 {
		t.Errorf("called EC2 with no region configured: %v", f.calls)
	}
}

func TestRegions_FallBackToAWSRegionVars(t *testing.T) {
	newFakeEC2(t)
	t.Setenv("AWS_DEFAULT_REGION", "ap-south-1")
	if got := regions(); len(got) != 1 || got[0] != "ap-south-1" {
		t.Errorf("regions() = %v, want AWS_DEFAULT_REGION", got)
	}
	t.Setenv("AWS_REGION", "eu-west-2")
	if got := regions(); len(got) != 1 || got[0] != "eu-west-2" {
		t.Errorf("regions() = %v, want AWS_REGION to win over AWS_DEFAULT_REGION", got)
	}
	t.Setenv("CORRAL_AWS_POWER_REGIONS", "us-west-2")
	if got := regions(); len(got) != 1 || got[0] != "us-west-2" {
		t.Errorf("regions() = %v, want the explicit list to win", got)
	}
}

func TestRun_StartStopActOnlyOnOptedInInstances(t *testing.T) {
	for verb, action := range map[string]string{"start": "StartInstances", "stop": "StopInstances"} {
		t.Run(verb, func(t *testing.T) {
			f := newFakeEC2(t)
			f.describe["eu-north-1"] = instanceXML("i-0a", "stopped", "lab")
			if err := run([]string{"host-power", verb, "eu-north-1/i-0a"}, io.Discard); err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			last := f.calls[len(f.calls)-1]
			if last.Get("Action") != action || last.Get("InstanceId.1") != "i-0a" || f.regions[len(f.regions)-1] != "eu-north-1" {
				t.Errorf("last call = %v in %s, want %s of i-0a in eu-north-1", last, f.regions[len(f.regions)-1], action)
			}
		})
	}
}

func TestRun_RefusesUntaggedInstance(t *testing.T) {
	f := newFakeEC2(t)
	f.describe["eu-north-1"] = instanceXML("i-0a", "running", "lab")
	err := run([]string{"host-power", "stop", "eu-north-1/i-0other"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is not tagged "+tagOptIn) {
		t.Fatalf("stop untagged = %v, want refusal", err)
	}
	for _, form := range f.calls {
		if a := form.Get("Action"); a != "DescribeInstances" {
			t.Errorf("issued %s against an instance that did not opt in", a)
		}
	}
}

func TestRun_SurfacesEC2ErrorCode(t *testing.T) {
	f := newFakeEC2(t)
	f.describe["eu-north-1"] = instanceXML("i-0a", "running", "lab")
	f.fail["StopInstances"] = http.StatusForbidden
	err := run([]string{"host-power", "stop", "eu-north-1/i-0a"}, io.Discard)
	if err == nil || err.Error() != "ec2 StopInstances: UnauthorizedOperation: not allowed" {
		t.Fatalf("err = %v, want the EC2 error code and message", err)
	}
}

func TestCall_NonXMLErrorFallsBackToStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	orig := endpoint
	endpoint = func(string) string { return srv.URL }
	t.Cleanup(func() { endpoint = orig })
	_, err := call(creds{AccessKey: "a", SecretKey: "b"}, "eu-north-1", "DescribeInstances", nil)
	if err == nil || err.Error() != "ec2 DescribeInstances: HTTP 502" {
		t.Errorf("err = %v, want HTTP 502", err)
	}
}

func TestCall_SessionTokenIsSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Amz-Security-Token")
		_, _ = w.Write([]byte(`<ok/>`))
	}))
	t.Cleanup(srv.Close)
	orig := endpoint
	endpoint = func(string) string { return srv.URL }
	t.Cleanup(func() { endpoint = orig })
	if _, err := call(creds{AccessKey: "a", SecretKey: "b", Token: "tok"}, "eu-north-1", "DescribeInstances", nil); err != nil {
		t.Fatal(err)
	}
	if got != "tok" {
		t.Errorf("X-Amz-Security-Token = %q, want the session token (STS credentials fail without it)", got)
	}
}

func TestRun_UsageAndCredentialErrors(t *testing.T) {
	newFakeEC2(t)
	if err := run([]string{"list"}, io.Discard); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("bad args = %v, want usage", err)
	}
	if err := run([]string{"host-power", "reboot"}, io.Discard); err == nil || !strings.Contains(err.Error(), `unknown host-power command "reboot"`) {
		t.Errorf("unknown verb = %v", err)
	}
	if err := run([]string{"host-power", "start"}, io.Discard); err == nil || !strings.Contains(err.Error(), "needs a host id") {
		t.Errorf("start without id = %v", err)
	}
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if err := run([]string{"host-power", "list"}, io.Discard); err == nil || !strings.Contains(err.Error(), "must be set") {
		t.Errorf("missing secret = %v, want credential error", err)
	}
}
