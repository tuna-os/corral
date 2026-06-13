//go:build bootc

// Package-local bootc plugin. Compiled only with `-tags bootc`; registers its
// implementations into the always-present seam in bootc_core.go. See that file
// for why bootc is opt-in.
package kubevirt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func init() {
	bootcBuildFunc = bootcBuildDisk
	bootcVMFunc = generateBootcVM
	bootcRebuildFunc = bootcRebuild
}

// bootcRebuild rebuilds name's bootc disk from imageURI and re-points its
// kernel boot at the result. Used by `corral bootc rebuild/switch/upgrade` and
// the web Upgrade button. Steps: stop the VM (frees the RWO disk) → delete the
// old disk PVC → build a fresh one from the image → patch the VM's kernelBoot
// (image + kernel paths + root UUID) → start it. Everything else on the VM
// (sizing, networks, node) is preserved.
func bootcRebuild(name, namespace, imageURI, sshPublicKey, diskSize string, progress io.Writer) error {
	if progress == nil {
		progress = os.Stderr
	}
	c := NewClient(namespace)
	pvc := name + "-bootc-disk"

	// Reuse the existing disk size unless the caller overrides it.
	if diskSize == "" {
		if out, err := runPkg("kubectl", "get", "pvc", pvc, "-n", namespace,
			"-o", "jsonpath={.spec.resources.requests.storage}"); err == nil {
			diskSize = strings.TrimSpace(string(out))
		}
	}

	fmt.Fprintf(progress, "Rebuilding %s from %s\n", name, imageURI)
	fmt.Fprintf(progress, "  Stopping VM to free the disk...\n")
	c.StopVM(name) // ignore: may already be stopped
	c.waitStopped(name)

	fmt.Fprintf(progress, "  Removing the old disk (%s)...\n", pvc)
	runPkg("kubectl", "delete", "pvc", pvc, "-n", namespace, "--ignore-not-found")
	runPkg("kubectl", "delete", "datavolume", pvc, "-n", namespace, "--ignore-not-found")
	// Wait for the PVC to actually disappear so the rebuild's fresh PVC isn't a
	// no-op against a terminating one.
	for i := 0; i < 60; i++ {
		if _, err := runPkg("kubectl", "get", "pvc", pvc, "-n", namespace); err != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}

	build, err := bootcBuildDisk(name, namespace, imageURI, sshPublicKey, diskSize, progress)
	if err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}

	// Re-point the kernel boot at the freshly built image/kernel/root. A merge
	// patch keeps the rest of the VM (sizing, NICs, node) intact.
	patch := fmt.Sprintf(
		`{"spec":{"template":{"spec":{"domain":{"firmware":{"kernelBoot":{"container":{"image":%q,"kernelPath":%q,"initrdPath":%q},"kernelArgs":%q}}}}}}}`,
		imageURI,
		fmt.Sprintf("/usr/lib/modules/%s/vmlinuz", build.KernelVersion),
		fmt.Sprintf("/usr/lib/modules/%s/initramfs.img", build.KernelVersion),
		fmt.Sprintf("root=UUID=%s ro console=ttyS0", build.RootUUID))
	if err := c.patchVMMerge(name, patch); err != nil {
		return fmt.Errorf("updating kernel boot: %w", err)
	}

	fmt.Fprintf(progress, "  Starting %s on the new disk...\n", name)
	return c.StartVM(name)
}

// bootcBuildDisk orchestrates the on-cluster bootc disk build pipeline.
// Progress and builder logs are written to progress (defaults to stderr),
// so callers like the web UI can capture them per-task.
func bootcBuildDisk(name, namespace, imageURI, sshPublicKey, diskSize string, progress io.Writer) (*BootcBuildResult, error) {
	if progress == nil {
		progress = os.Stderr
	}
	if diskSize == "" {
		diskSize = "50Gi"
	}

	pvcName := name + "-bootc-disk"
	jobName := name + "-bootc-builder"

	fmt.Fprintf(progress, "Building bootc disk from %s\n", imageURI)
	fmt.Fprintf(progress, "  PVC:    %s (%s)\n", pvcName, diskSize)

	// Step 1: Create PVC
	pvc := GeneratePVCWithClass(pvcName, namespace, diskSize, PreferredStorageClass())
	data, err := json.Marshal(pvc)
	if err != nil {
		return nil, fmt.Errorf("marshaling PVC: %w", err)
	}
	if err := applyManifest(string(data)); err != nil {
		return nil, fmt.Errorf("creating PVC: %w", err)
	}

	// Step 2: Create and run the builder Job
	sshKeyB64 := base64.StdEncoding.EncodeToString([]byte(sshPublicKey))
	jobYAML := generateBootcJob(jobName, namespace, imageURI, pvcName, sshKeyB64, diskSize)
	if err := applyManifest(jobYAML); err != nil {
		return nil, fmt.Errorf("creating builder job: %w", err)
	}

	// Step 3: Wait for Job completion, tailing logs
	fmt.Fprintf(progress, "  Waiting for builder pod...\n")
	vars, err := waitForBootcJob(jobName, namespace, progress)
	if err != nil {
		exec.Command("kubectl", "delete", "job", jobName, "-n", namespace, "--ignore-not-found").Run()
		return nil, fmt.Errorf("bootc build failed: %w", err)
	}

	// Step 4: Clean up Job
	exec.Command("kubectl", "delete", "job", jobName, "-n", namespace, "--ignore-not-found").Run()

	res := &BootcBuildResult{
		PVCName:       pvcName,
		RootUUID:      vars["ROOT_UUID"],
		KernelVersion: vars["KERNEL_VERSION"],
	}
	if res.RootUUID == "" {
		return nil, fmt.Errorf("ROOT_UUID not found in builder logs")
	}
	if res.KernelVersion == "" {
		return nil, fmt.Errorf("KERNEL_VERSION not found in builder logs")
	}
	fmt.Fprintf(progress, "  Disk ready: pvc/%s (root UUID: %s, kernel: %s)\n",
		res.PVCName, res.RootUUID, res.KernelVersion)
	return res, nil
}

// generateBootcJob creates a Kubernetes Job that builds a bootc disk image
// using bootc install to-filesystem on a raw XFS loopback device.
//
// The image file MUST be named disk.img: that's KubeVirt's convention for
// filesystem-mode PVC disks — any other name and virt-handler ignores the
// built system and tries to create a blank disk.img instead (which also
// fails for space). And it must be sized below the PVC's usable capacity
// (requested size minus filesystem overhead), or that blank-disk fallback
// hits "not enough space".
func generateBootcJob(name, namespace, imageURI, pvcName, sshKeyB64, diskSize string) string {
	sizeVal := strings.TrimSuffix(strings.TrimSuffix(diskSize, "Gi"), "G")
	if sizeVal == diskSize {
		sizeVal = strings.TrimSuffix(strings.TrimSuffix(diskSize, "Mi"), "M")
	}
	if sizeVal == diskSize {
		sizeVal = "50"
	}
	sizeGB, err := strconv.Atoi(sizeVal)
	if err != nil || sizeGB < 1 {
		sizeGB = 50
	}
	// 85% of the PVC leaves room for filesystem overhead on the PVC itself.
	imgGB := sizeGB * 85 / 100
	if imgGB < 1 {
		imgGB = 1
	}

	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app: corral-bootc-builder
spec:
  backoffLimit: 1
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels:
        app: corral-bootc-builder
    spec:
      restartPolicy: Never
      initContainers:
        - name: create-disk
          image: alpine:latest
          command: ["sh", "-c", "rm -f /output/disk.img /output/disk.raw && truncate -s %dG /output/disk.img"]
          volumeMounts:
            - name: output
              mountPath: /output
      containers:
        - name: builder
          image: %s
          command: ["/bin/bash", "-c"]
          args:
            - |
              set -euo pipefail
              echo "=== Bootc install: %s ==="
              echo "$SSH_KEY" | base64 -d > /var/tmp/authorized_keys
              echo "SSH key written"

              KERNEL_VERSION=$(ls /usr/lib/modules | sort -V | tail -n1)
              echo "KERNEL_VERSION=$KERNEL_VERSION"

              # Loop allocation can fail transiently — the loop module/device
              # nodes may not be present yet on a fresh node. Nudge them into
              # existence and retry generously before failing to the Job backoff.
              modprobe loop 2>/dev/null || true
              [ -e /dev/loop-control ] || mknod /dev/loop-control c 10 237 2>/dev/null || true
              LOOP=""
              for i in $(seq 1 20); do
                LOOP=$(losetup -f --show /output/disk.img 2>/dev/null) && break
                echo "losetup attempt $i failed; retrying in 3s"
                sleep 3
              done
              [ -n "$LOOP" ] || { echo "ERROR: no loop device available after 20 attempts"; exit 1; }
              echo "Loop device: $LOOP"

              mkfs.xfs -f "$LOOP"
              mkdir -p /target
              mount "$LOOP" /target

              ROOT_UUID=$(blkid -s UUID -o value "$LOOP")
              echo "ROOT_UUID=$ROOT_UUID"

              bootc install to-filesystem \
                --source-imgref=docker://%s \
                --root-ssh-authorized-keys=/var/tmp/authorized_keys \
                --generic-image \
                --bootloader none \
                --karg=root=UUID=$ROOT_UUID \
                /target 2>&1

              DEPLOY_ROOT=$(ls -d /target/ostree/deploy/*/deploy/*.0 2>/dev/null | head -n1 || true)
              if [ -n "$DEPLOY_ROOT" ]; then
                systemctl --root="$DEPLOY_ROOT" enable sshd.service \
                  && echo "sshd enabled" || echo "WARNING: could not enable sshd"
              else
                echo "WARNING: no ostree deployment found, sshd not enabled"
              fi

              echo "=== INSTALL COMPLETE ==="
          env:
            - name: SSH_KEY
              value: "%s"
          securityContext:
            privileged: true
          volumeMounts:
            - name: output
              mountPath: /output
      volumes:
        - name: output
          persistentVolumeClaim:
            claimName: %s
`, name, namespace, imgGB, imageURI, name, imageURI, sshKeyB64, pvcName)
}

// waitForBootcJob waits for a Job to complete, streaming builder logs to
// stderr, and returns the KEY=VALUE variables echoed by the build script.
func waitForBootcJob(name, namespace string, progress io.Writer) (map[string]string, error) {
	// The Job retries on failure with a *fresh* pod, so never latch onto one
	// pod name — always resolve the newest pod for this job.
	newestPod := func() string {
		out, err := exec.Command("kubectl", "get", "pod", "-n", namespace,
			"-l", "job-name="+name,
			"--sort-by=.metadata.creationTimestamp",
			"-o", "jsonpath={.items[-1:].metadata.name}").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	var podName string
	for i := 0; i < 60 && podName == ""; i++ {
		podName = newestPod()
		if podName == "" {
			time.Sleep(2 * time.Second)
		}
	}
	if podName == "" {
		return nil, fmt.Errorf("builder pod not found after 2 minutes")
	}

	// Wait for the builder container to start. The bootc image pull dominates
	// this phase and can take 10+ minutes for multi-GB images on home links,
	// so wait generously and report state changes (not a spam line per poll).
	lastReason, started := "", false
	for i := 0; i < 450 && !started; i++ {
		if p := newestPod(); p != "" && p != podName {
			fmt.Fprintf(progress, "  Builder pod retried: %s → %s\n", podName, p)
			podName = p
			lastReason = ""
		}
		ready, _ := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
			"-o", "jsonpath={.status.containerStatuses[?(@.name==\"builder\")].started}").Output()
		started = strings.TrimSpace(string(ready)) == "true"
		if started {
			break
		}
		reason, _ := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
			"-o", "jsonpath={.status.containerStatuses[?(@.name==\"builder\")].state.waiting.reason}").Output()
		if r := strings.TrimSpace(string(reason)); r != "" && r != lastReason {
			fmt.Fprintf(progress, "  Pod initializing: builder (%s)\n", r)
			lastReason = r
		} else if i > 0 && i%30 == 0 {
			fmt.Fprintf(progress, "  Still waiting for the builder container (%s, %dm — large bootc images take a while to pull)\n",
				lastReason, i/30)
		}
		time.Sleep(2 * time.Second)
	}
	if !started {
		return nil, fmt.Errorf("builder container did not start within 15 minutes (image pull too slow or stuck) — check: kubectl describe pod %s -n %s", podName, namespace)
	}

	fmt.Fprintf(progress, "  Builder running — streaming logs...\n")
	stream := exec.Command("kubectl", "logs", "-f", podName, "-n", namespace, "-c", "builder")
	stream.Stdout = progress
	stream.Stderr = progress
	stream.Start()
	defer func() {
		if stream.Process != nil {
			stream.Process.Kill()
			stream.Wait()
		}
	}()

	// Poll for job completion (up to 15 minutes)
	for i := 0; i < 450; i++ {
		cond, _ := exec.Command("kubectl", "get", "job", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type==\"Complete\")].status}").Output()
		if strings.TrimSpace(string(cond)) == "True" {
			// A retry mid-build means the vars live in the newest pod's logs.
			if p := newestPod(); p != "" {
				podName = p
			}
			return parseBuildVars(podName, namespace)
		}
		failed, _ := exec.Command("kubectl", "get", "job", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type==\"Failed\")].status}").Output()
		if strings.TrimSpace(string(failed)) == "True" {
			return nil, fmt.Errorf("builder job failed — check logs: kubectl logs -n %s job/%s", namespace, name)
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("timeout waiting for bootc build (15 minutes)")
}

// parseBuildVars extracts KEY=VALUE lines (ROOT_UUID, KERNEL_VERSION) from pod logs.
func parseBuildVars(podName, namespace string) (map[string]string, error) {
	out, err := exec.Command("kubectl", "logs", podName, "-n", namespace, "-c", "builder").Output()
	if err != nil {
		return nil, fmt.Errorf("reading pod logs: %w", err)
	}
	vars := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		for _, key := range []string{"ROOT_UUID", "KERNEL_VERSION"} {
			if strings.HasPrefix(line, key+"=") {
				vars[key] = strings.TrimSpace(strings.TrimPrefix(line, key+"="))
			}
		}
	}
	return vars, nil
}

// generateBootcVM creates a KubeVirt VirtualMachine manifest for a bootc-built disk.
// Uses kernel boot (vmlinuz+initrd pulled from the bootc image at the detected
// kernel version) since GRUB can't install on loopback devices with this
// cluster's kernel.
func generateBootcVM(name, namespace, pvcName, imageURI, rootUUID, kernelVersion, mem string, cpu int, node, tailscaleAuthKey string) map[string]any {
	if mem == "" {
		mem = "4G"
	}
	if cpu == 0 {
		cpu = 2
	}

	memMib := parseMem(mem)
	kernelArgs := fmt.Sprintf("root=UUID=%s ro console=ttyS0", rootUUID)

	volumes := []map[string]any{
		{
			"name":                  "rootdisk",
			"persistentVolumeClaim": map[string]any{"claimName": pvcName},
		},
	}

	// Bootc VMs don't have cloud-init by default (SSH key is baked into the
	// disk during bootc install). Add a cloudinitdisk only when Tailscale is
	// configured so the VM auto-joins the tailnet on first boot.
	if tailscaleAuthKey != "" {
		runcmd := fmt.Sprintf(`runcmd:
  - ['sh', '-c', 'command -v tailscale >/dev/null 2>&1 || curl -fsSL https://tailscale.com/install.sh | sh']
  - ['tailscale', 'up', '--auth-key=%s', '--hostname=%s']
`, tailscaleAuthKey, name)
		volumes = append(volumes, map[string]any{
			"name": "cloudinitdisk",
			"cloudInitNoCloud": map[string]any{
				"userData": runcmd,
			},
		})
	}

	spec := map[string]any{
		"running": false,
		"template": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"kubevirt.io/vm": name},
			},
			"spec": map[string]any{
				"domain": map[string]any{
					"cpu":    cpuSpec(cpu),
					"memory": memSpec(memMib),
					"firmware": map[string]any{
						"kernelBoot": map[string]any{
							"container": map[string]any{
								"image":      imageURI,
								"kernelPath": fmt.Sprintf("/usr/lib/modules/%s/vmlinuz", kernelVersion),
								"initrdPath": fmt.Sprintf("/usr/lib/modules/%s/initramfs.img", kernelVersion),
							},
							"kernelArgs": kernelArgs,
						},
					},
					"devices": map[string]any{
						"disks": []map[string]any{
							{
								"name": "rootdisk",
								"disk": map[string]any{"bus": "virtio"},
							},
						},
						"interfaces": []map[string]any{
							{"name": "default", "masquerade": map[string]any{}},
						},
					},
				},
				"networks": []map[string]any{
					{"name": "default", "pod": map[string]any{}},
				},
				"volumes": volumes,
			},
		},
	}

	if node != "" {
		spec["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"] = map[string]any{
			"kubernetes.io/hostname": node,
		}
	}

	return map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    map[string]any{"corral": name},
		},
		"spec": spec,
	}
}
