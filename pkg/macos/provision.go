package macos

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Provision prepares media and owns the loopback-only CDI tunnel for a new VM.
// An existing VM takes Create's preservation path without downloads or a tunnel.
func (c Client) Provision(o Options, kubeContext string, progress io.Writer) error {
	ctx := c.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(kubeContext) == "" {
		return errors.New("provisioning requires an explicit kubeconfig context")
	}
	if err := validate(o); err != nil {
		return err
	}
	vm, err := c.get("vm", o.Name, o.Namespace)
	if err != nil {
		return err
	}
	if vm != nil {
		return c.Create(o)
	}
	if o.Node == "" {
		return errors.New("--node is required for new local-storage provisioning")
	}
	if err := c.requirements(o.Namespace, o.EnableSidecar); err != nil {
		return err
	}
	if progress != nil {
		_, _ = fmt.Fprintln(progress, "Preparing verified Sequoia recovery and private EFI dependencies.")
	}
	if _, err := PrepareMedia(ctx, o.MediaDir); err != nil {
		return err
	}
	proxy, stop, err := startUploadProxy(ctx, kubeContext)
	if err != nil {
		return err
	}
	defer stop()
	o.UploadProxyURL = proxy
	return c.Create(o)
}

func startUploadProxy(ctx context.Context, kubeContext string) (string, func(), error) {
	proxyContext, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(proxyContext, "kubectl", "--context", kubeContext, "-n", "cdi", "port-forward", "--address", "127.0.0.1", "svc/cdi-uploadproxy", ":443")
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return "", nil, errors.New("cannot open CDI port-forward output")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		return "", nil, errors.New("cannot start loopback CDI port-forward")
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	stop := func() {
		cancel()
		_ = stdout.Close()
		<-done
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			const prefix = "Forwarding from 127.0.0.1:"
			line := scanner.Text()
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(line, prefix))
			if len(fields) != 3 || fields[1] != "->" {
				continue
			}
			port, err := strconv.Atoi(fields[0])
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			select {
			case ready <- "https://127.0.0.1:" + strconv.Itoa(port):
			default:
			}
		}
	}()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case address := <-ready:
		select {
		case <-done:
			stop()
			return "", nil, errors.New("CDI port-forward exited before use")
		default:
			return address, stop, nil
		}
	case <-done:
		stop()
		return "", nil, errors.New("CDI port-forward exited before binding a loopback port")
	case <-ctx.Done():
		stop()
		return "", nil, ctx.Err()
	case <-timer.C:
		stop()
		return "", nil, errors.New("CDI loopback port-forward did not become ready within 30 seconds")
	}
}
