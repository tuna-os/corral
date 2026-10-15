package nutanix

// Inventory and identity.
//
// Prism Central addresses VMs by extId (a UUID); Corral addresses everything
// by name. The v4 list call returns every AHV VM PC manages — name, power
// state, shape, NICs, cluster and host refs — in one OData-pageable request,
// so the name→extId map and the fleet listing are the same pages, and no
// operation needs a second round trip to find out where an instance lives.
// Name collisions resolve explicitly: Resolve refuses an ambiguous name
// rather than picking a VM for the caller.

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tuna-os/corral/pkg/types"
)

// Ref is a v4 entity reference: every cross-entity link is an extId.
type Ref struct {
	ExtID string `json:"extId"`
}

// NIC is the subset of a v4 VM NIC Corral reads: the first IPv4 for the
// listing, the MAC for identity debugging.
type NIC struct {
	Backing *struct {
		MacAddress string `json:"macAddress"`
	} `json:"backingInfo"`
	Network *struct {
		IPv4 *struct {
			IPAddress *struct {
				Value string `json:"value"`
			} `json:"ipAddress"`
		} `json:"ipv4Config"`
	} `json:"networkInfo"`
}

// VM is one AHV VM as the v4 vmm namespace reports it: the subset of
// /ahv/config/vms Corral maps. Fields PC does not return (host name — only
// the host extId — live CPU percent, disk usage) are simply absent, and the
// mapping below must not invent them.
type VM struct {
	ExtID             string `json:"extId"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	PowerState        string `json:"powerState"` // ON, OFF, PAUSED, ...
	NumSockets        int    `json:"numSockets"`
	NumCoresPerSocket int    `json:"numCoresPerSocket"`
	MemorySizeBytes   int64  `json:"memorySizeBytes"`
	NICs              []NIC  `json:"nics"`
	Cluster           *Ref   `json:"cluster"`
	Host              *Ref   `json:"host"`
	Categories        []Ref  `json:"categories"`
}

// listResponse is the v4 list envelope: data plus OData metadata. The page
// count comes from metadata.totalAvailableResults, not from guessing.
type listResponse struct {
	Data []VM `json:"data"`
	Meta struct {
		TotalAvailableResults int `json:"totalAvailableResults"`
	} `json:"metadata"`
}

// getResponse is the single-entity envelope.
type getResponse struct {
	Data VM `json:"data"`
}

// vmmPath prefixes the versioned vmm namespace.
func (c *Client) vmmPath(suffix string) string {
	return "/vmm/" + c.cfg.APIVersion + "/ahv/config/vms" + suffix
}

// List converts every AHV VM Prism Central manages into Corral VMs,
// following every page until the total is reached. The API caps a page at
// 100, so a large PC always pages.
func (c *Client) List() ([]types.VM, error) {
	const limit = 100
	var vms []types.VM
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("$page", strconv.Itoa(page))
		q.Set("$limit", strconv.Itoa(limit))
		var resp listResponse
		if err := c.get(c.vmmPath("?"+q.Encode()), &resp); err != nil {
			return nil, err
		}
		for _, vm := range resp.Data {
			vms = append(vms, c.toVM(vm))
		}
		if len(resp.Data) < limit || len(vms) >= resp.Meta.TotalAvailableResults {
			return vms, nil
		}
	}
}

// Get fetches one VM by extId.
func (c *Client) Get(extID string) (VM, error) {
	var resp getResponse
	if err := c.get(c.vmmPath("/"+extID), &resp); err != nil {
		return VM{}, err
	}
	return resp.Data, nil
}

// Resolve finds a VM by name via a server-side exact-match filter. An empty
// result is "no such VM"; more than one is ambiguous and refused, because
// two AHV VMs may share a name across clusters and guessing between them is
// how the wrong guest gets powered off.
func (c *Client) Resolve(name string) (VM, error) {
	q := url.Values{}
	q.Set("$filter", "name eq '"+odataEscape(name)+"'")
	var resp listResponse
	if err := c.get(c.vmmPath("?"+q.Encode()), &resp); err != nil {
		return VM{}, err
	}
	switch len(resp.Data) {
	case 0:
		return VM{}, fmt.Errorf("nutanix: no VM named %q", name)
	case 1:
		return resp.Data[0], nil
	default:
		return VM{}, fmt.Errorf("nutanix: %d VMs named %q; use the extId", len(resp.Data), name)
	}
}

// Exists reports whether a VM with the given name is present. A Prism
// Central that cannot be reached reports false, matching every other
// backend's Exists.
func (c *Client) Exists(name string) bool {
	_, err := c.Resolve(name)
	return err == nil
}

func odataEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func (c *Client) toVM(vm VM) types.VM {
	out := types.VM{
		Name:    displayName(vm),
		Backend: "nutanix",
		// Namespace stays empty: PC has no namespace, and repurposing the
		// field (for the cluster extId) would make identity mean two
		// things — the proxmoxbe precedent. Node stays empty too: the list
		// carries only the host extId, not its name, and an extId is not a
		// node a human recognises.
		Context: c.cfg.Host,
		Status:  statusWord(vm.PowerState),
		Running: strings.EqualFold(vm.PowerState, "ON"),
		CPU:     vm.NumSockets * orOne(vm.NumCoresPerSocket),
		Mem:     humanBytes(vm.MemorySizeBytes),
		IP:      firstIPv4(vm),
	}
	out.Ready = out.Running
	out.SetIdentity()
	return out
}

// displayName falls back to the extId when a VM has no name, which v4
// allows.
func displayName(vm VM) string {
	if strings.TrimSpace(vm.Name) != "" {
		return vm.Name
	}
	return vm.ExtID
}

// statusWord maps the v4 powerState enum to the capitalised words the TUI
// classifies on. Unknown states pass through capitalised rather than
// collapsing to Unknown: a new enum value should be visible, not hidden.
func statusWord(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "ON":
		return "Running"
	case "OFF":
		return "Stopped"
	case "PAUSED":
		return "Paused"
	case "":
		return "Unknown"
	default:
		return strings.ToUpper(state[:1]) + strings.ToLower(state[1:])
	}
}

func orOne(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

func firstIPv4(vm VM) string {
	for _, nic := range vm.NICs {
		if nic.Network == nil || nic.Network.IPv4 == nil || nic.Network.IPv4.IPAddress == nil {
			continue
		}
		if ip := strings.TrimSpace(nic.Network.IPv4.IPAddress.Value); ip != "" {
			return ip
		}
	}
	return ""
}

func humanBytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
