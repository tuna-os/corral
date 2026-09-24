// corral-aws-power is a host-power plugin (sdk.CapHostPower) for EC2: it lets
// Corral power on and off the instances that host KubeVirt VMs, so a VM node
// can stay stopped (and cost nothing but its disk) until someone needs it.
//
// Which instances it manages is decided by tags, not by Corral config:
//
//	corral:host-power = <display name>   opt the instance in (value may be empty)
//	corral:node       = <k8s node name>  optional; defaults to the first label
//	                                     of the instance's private DNS name
//
// Regions come from CORRAL_AWS_POWER_REGIONS (comma-separated) or AWS_REGION.
// Credentials are the standard AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY /
// AWS_SESSION_TOKEN; scope them to ec2:DescribeInstances plus
// ec2:StartInstances/StopInstances on the tagged instances.
//
//	corral aws-power host-power list
//	corral aws-power host-power start eu-north-1/i-0123456789abcdef0
//	corral aws-power host-power stop  eu-north-1/i-0123456789abcdef0
//
// EC2 is called through its Query API with a small SigV4 signer instead of the
// AWS SDK: three calls do not justify the dependency tree.
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/plugin/sdk"
)

const (
	tagOptIn = "corral:host-power"
	tagNode  = "corral:node"
)

type creds struct{ AccessKey, SecretKey, Token string }

// endpoint is overridable for tests.
var endpoint = func(region string) string { return "https://ec2." + region + ".amazonaws.com/" }

var client = &http.Client{Timeout: 20 * time.Second}

func main() {
	if sdk.HandleMetadata(sdk.Metadata{
		Name:              "aws-power",
		Version:           "0.1.0",
		Description:       "Power EC2 instances that host VMs on and off (host-power hook)",
		Capabilities:      []string{sdk.CapHostPower, "cli-command"},
		Permissions:       []string{"aws:ec2:DescribeInstances", "aws:ec2:StartInstances", "aws:ec2:StopInstances"},
		SupportedBackends: []string{"all"},
	}) {
		return
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "corral-aws-power:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) < 2 || args[0] != "host-power" {
		return fmt.Errorf("usage: corral-aws-power host-power list | start <region/instance-id> | stop <region/instance-id>")
	}
	c := creds{os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")}
	if c.AccessKey == "" || c.SecretKey == "" {
		return fmt.Errorf("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set")
	}
	switch args[1] {
	case "list":
		hosts := []sdk.Host{}
		for _, region := range regions() {
			hs, err := list(c, region)
			if err != nil {
				return err
			}
			hosts = append(hosts, hs...)
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
		return json.NewEncoder(out).Encode(hosts)
	case "start", "stop":
		if len(args) != 3 {
			return fmt.Errorf("%s needs a host id (region/instance-id)", args[1])
		}
		region, id, ok := strings.Cut(args[2], "/")
		if !ok || region == "" || !strings.HasPrefix(id, "i-") {
			return fmt.Errorf("invalid host id %q (want region/instance-id)", args[2])
		}
		// Only act on instances that opted in: the tag is the permission
		// boundary even if the IAM policy is broader than it should be.
		hosts, err := list(c, region)
		if err != nil {
			return err
		}
		found := false
		for _, h := range hosts {
			found = found || h.ID == args[2]
		}
		if !found {
			return fmt.Errorf("%s is not tagged %s", args[2], tagOptIn)
		}
		action := map[string]string{"start": "StartInstances", "stop": "StopInstances"}[args[1]]
		_, err = call(c, region, action, url.Values{"InstanceId.1": {id}})
		return err
	}
	return fmt.Errorf("unknown host-power command %q", args[1])
}

func regions() []string {
	var rs []string
	for _, r := range strings.Split(os.Getenv("CORRAL_AWS_POWER_REGIONS"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 {
		for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
			if r := os.Getenv(k); r != "" {
				return []string{r}
			}
		}
	}
	return rs
}

type describeResp struct {
	Instances []struct {
		ID             string `xml:"instanceId"`
		Type           string `xml:"instanceType"`
		State          string `xml:"instanceState>name"`
		PrivateDNSName string `xml:"privateDnsName"`
		Tags           []struct {
			Key   string `xml:"key"`
			Value string `xml:"value"`
		} `xml:"tagSet>item"`
	} `xml:"reservationSet>item>instancesSet>item"`
}

func list(c creds, region string) ([]sdk.Host, error) {
	body, err := call(c, region, "DescribeInstances", url.Values{
		"Filter.1.Name":    {"tag-key"},
		"Filter.1.Value.1": {tagOptIn},
		"Filter.2.Name":    {"instance-state-name"},
		"Filter.2.Value.1": {"pending"}, "Filter.2.Value.2": {"running"},
		"Filter.2.Value.3": {"stopping"}, "Filter.2.Value.4": {"stopped"},
	})
	if err != nil {
		return nil, err
	}
	return parseHosts(body, region)
}

func parseHosts(body []byte, region string) ([]sdk.Host, error) {
	var res describeResp
	if err := xml.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("parsing DescribeInstances: %w", err)
	}
	hosts := []sdk.Host{}
	for _, in := range res.Instances {
		tags := map[string]string{}
		for _, t := range in.Tags {
			tags[t.Key] = t.Value
		}
		name := tags[tagOptIn]
		if name == "" {
			name = tags["Name"]
		}
		if name == "" {
			name = in.ID
		}
		node := tags[tagNode]
		if node == "" && in.PrivateDNSName != "" {
			node, _, _ = strings.Cut(in.PrivateDNSName, ".")
		}
		h := sdk.Host{
			ID:     region + "/" + in.ID,
			Name:   name,
			Node:   node,
			Detail: in.Type + " · " + region,
		}
		switch in.State {
		case "running":
			h.State, h.Actions = "running", []string{"stop"}
		case "stopped":
			h.State, h.Actions = "stopped", []string{"start"}
		case "pending":
			h.State = "starting"
		case "stopping", "shutting-down":
			h.State = "stopping"
		default:
			h.State = "unknown"
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func call(c creds, region, action string, params url.Values) ([]byte, error) {
	form := url.Values{"Action": {action}, "Version": {"2016-11-15"}}
	for k, v := range params {
		form[k] = v
	}
	payload := form.Encode()
	req, err := http.NewRequest(http.MethodPost, endpoint(region), strings.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sign(req, []byte(payload), c, region, "ec2", time.Now().UTC())
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ec2 %s: %w", action, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code    string `xml:"Errors>Error>Code"`
			Message string `xml:"Errors>Error>Message"`
		}
		if xml.Unmarshal(body, &e) == nil && e.Code != "" {
			return nil, fmt.Errorf("ec2 %s: %s: %s", action, e.Code, e.Message)
		}
		return nil, fmt.Errorf("ec2 %s: HTTP %d", action, resp.StatusCode)
	}
	return body, nil
}
