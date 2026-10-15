package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/vdi"
)

// cliIdentity is the actor of a connect that runs on kubeconfig
// credentials: whoever holds them may act on every desktop.
const cliIdentity = "corral-vdi/cli"

// connectCmd resolves a claimed desktop to the one console to open (#229),
// with the same rules as the web Connect action (vdi.ResolveConnect).
func connectCmd() *cobra.Command {
	var kind, broker, user string
	var open bool
	c := &cobra.Command{
		Use:   "connect <member>",
		Short: "Open or print the best console of a claimed desktop",
		Long: `Resolve a claimed desktop to one console: browser RDP when the guest answers
on TCP 3389, then VNC, then the serial console (a CT offers its terminal only).

With --broker, ask the self-service broker of corral web. It takes your
identity from the Tailscale ingress, checks that you hold the desktop, and
probes the guest from the cluster. --open then opens the desktop in the web UI.

Without --broker, use your kubeconfig credentials, which may act on every
desktop; --user checks that the desktop belongs to that user. The RDP probe
runs from this machine. --open then starts xfreerdp, virtctl vnc, virtctl
console, or a CT shell.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: useKubeContext,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := vdi.MemberRef{Kind: vdi.PoolKind(kind), Namespace: nsOrDefault(), Name: args[0]}
			var target vdi.ConnectTarget
			var err error
			if broker != "" {
				target, err = connectViaBroker(cmd.Context(), http.DefaultClient, broker, ref)
			} else {
				target, err = connectViaCluster(ref, user)
			}
			if err != nil {
				return err
			}
			plan := planConnect(target, ref, broker)
			if _, err := fmt.Fprint(cmd.OutOrStdout(), plan.text); err != nil {
				return err
			}
			if !open {
				return nil
			}
			return runConnect(plan, ref)
		},
	}
	c.Flags().StringVar(&kind, "kind", string(vdi.PoolKindVM), "Pool kind of the member: vm or ct")
	c.Flags().StringVar(&broker, "broker", os.Getenv("CORRAL_VDI_BROKER_URL"), "URL of corral web with the VDI broker (default $CORRAL_VDI_BROKER_URL)")
	c.Flags().StringVarP(&user, "user", "u", "", "Without --broker: require that this user holds the desktop")
	c.Flags().BoolVar(&open, "open", false, "Open the console instead of only printing it")
	return c
}

// connectViaBroker asks corral web's broker for the target.
func connectViaBroker(ctx context.Context, client *http.Client, broker string, ref vdi.MemberRef) (vdi.ConnectTarget, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := strings.TrimRight(broker, "/") + "/api/vdi/desktops/" + string(ref.Kind) + "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name) + "/connect"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return vdi.ConnectTarget{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return vdi.ConnectTarget{}, fmt.Errorf("reach the VDI broker: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return vdi.ConnectTarget{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(body))
		}
		return vdi.ConnectTarget{}, fmt.Errorf("VDI broker: %s (HTTP %d)", e.Error, resp.StatusCode)
	}
	var t vdi.ConnectTarget
	if err := json.Unmarshal(body, &t); err != nil {
		return vdi.ConnectTarget{}, fmt.Errorf("decode the broker's answer: %w", err)
	}
	return t, nil
}

// connectViaCluster resolves the target with kubeconfig credentials.
func connectViaCluster(ref vdi.MemberRef, user string) (vdi.ConnectTarget, error) {
	actor := vdi.Actor{Identity: cliIdentity, Admin: true}
	if user != "" {
		actor = vdi.Actor{Identity: user}
	}
	b := vdi.NewBroker(vdi.KubeCluster(), vdi.BrokerConfig{Namespaces: []string{ref.Namespace}})
	return b.Connect(actor, ref, vdi.ProbeRDP)
}

// connectPlan is what connect prints, and the command --open runs.
type connectPlan struct {
	text string
	argv []string
	// ctShell marks a CT terminal, whose shell --open reads from the CT.
	ctShell bool
}

func browserOpener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func planConnect(t vdi.ConnectTarget, ref vdi.MemberRef, broker string) connectPlan {
	var b strings.Builder
	fmt.Fprintf(&b, "desktop %s (held by %s)\n", t.Desktop.ID, t.Desktop.Owner)
	var p connectPlan
	switch {
	case broker != "":
		// The web UI's address of the Desktops screen, which connects to the
		// desktop it names.
		link := strings.TrimRight(broker, "/") + "/#sel=desktops:" + ref.ID()
		fmt.Fprintf(&b, "connect: %s in the browser: %s\n", t.Protocol, link)
		p.argv = []string{browserOpener(), link}
	case t.Protocol == vdi.ProtocolRDP:
		fmt.Fprintf(&b, "connect: RDP to %s (any RDP client, e.g. xfreerdp /v:%s)\n", t.Direct, t.Direct)
		p.argv = []string{"xfreerdp", "/v:" + t.Direct}
	case t.Protocol == vdi.ProtocolVNC:
		fmt.Fprintf(&b, "connect: VNC: virtctl vnc %s -n %s\n", ref.Name, ref.Namespace)
		p.argv = []string{"virtctl", "vnc", ref.Name, "-n", ref.Namespace}
	case ref.Kind == vdi.PoolKindCT:
		fmt.Fprintf(&b, "connect: terminal: kubectl exec -it %s -n %s\n", ref.Name, ref.Namespace)
		p.ctShell = true
	default:
		fmt.Fprintf(&b, "connect: serial console: virtctl console %s -n %s\n", ref.Name, ref.Namespace)
		p.argv = []string{"virtctl", "console", ref.Name, "-n", ref.Namespace}
	}
	for _, f := range t.Fallbacks {
		fmt.Fprintf(&b, "fallback: %s\n", f.Protocol)
	}
	for _, s := range t.Skipped {
		fmt.Fprintf(&b, "skipped: %s\n", s)
	}
	p.text = b.String()
	return p
}

func runConnect(p connectPlan, ref vdi.MemberRef) error {
	kubeContext, err := ctPoolKubeContext()
	if err != nil {
		return err
	}
	argv := p.argv
	if p.ctShell {
		shellCmd, err := ct.ExecCommand(ref.Name, ref.Namespace)
		if err != nil {
			return err
		}
		argv = append([]string{"kubectl", "exec", "-i", "-t", ref.Name, "-n", ref.Namespace, "--"}, shellCmd...)
	}
	var cmd *exec.Cmd
	switch argv[0] {
	case "virtctl", "kubectl":
		cmd = shell.CommandForContext(kubeContext, argv[0], argv[1:]...)
	default:
		if _, err := exec.LookPath(argv[0]); err != nil {
			return fmt.Errorf("%s is not installed; run the printed command with another client", argv[0])
		}
		cmd = exec.Command(argv[0], argv[1:]...)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
