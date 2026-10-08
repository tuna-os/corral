package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS's published SigV4 example (IAM ListUsers, 2015-08-30), the vector the
// SigV4 documentation walks through step by step.
func TestSignMatchesAWSExample(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	c := creds{AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	sign(req, nil, c, "us-east-1", "iam", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization:\n got %s\nwant %s", got, want)
	}
}

func TestParseHosts(t *testing.T) {
	body := []byte(`<DescribeInstancesResponse><reservationSet><item><instancesSet>
	<item><instanceId>i-0a</instanceId><instanceType>m7i.2xlarge</instanceType>
	  <instanceState><name>stopped</name></instanceState>
	  <privateDnsName>ip-10-20-1-12.eu-north-1.compute.internal</privateDnsName>
	  <tagSet><item><key>corral:host-power</key><value>KubeVirt (AWS)</value></item></tagSet></item>
	<item><instanceId>i-0b</instanceId><instanceType>c8i.large</instanceType>
	  <instanceState><name>pending</name></instanceState>
	  <tagSet><item><key>corral:host-power</key><value></value></item>
	          <item><key>Name</key><value>lab</value></item>
	          <item><key>corral:node</key><value>lab-node</value></item></tagSet></item>
	</instancesSet></item></reservationSet></DescribeInstancesResponse>`)
	hosts, err := parseHosts(body, "eu-north-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("got %d hosts", len(hosts))
	}
	a, b := hosts[0], hosts[1]
	if a.ID != "eu-north-1/i-0a" || a.Name != "KubeVirt (AWS)" || a.Node != "ip-10-20-1-12" ||
		a.State != "stopped" || strings.Join(a.Actions, ",") != "start" {
		t.Errorf("stopped host: %+v", a)
	}
	if b.Name != "lab" || b.Node != "lab-node" || b.State != "starting" || len(b.Actions) != 0 {
		t.Errorf("pending host: %+v", b)
	}
}

func TestRunRejectsBadIDs(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")
	for _, id := range []string{"i-0a", "eu-north-1/", "eu-north-1/vol-1", "/i-0a"} {
		if err := run([]string{"host-power", "start", id}, nil); err == nil || !strings.Contains(err.Error(), "invalid host id") {
			t.Errorf("%q: want invalid host id, got %v", id, err)
		}
	}
}
