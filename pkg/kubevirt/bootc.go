package kubevirt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BootcBuildResult describes a finished on-cluster bootc disk build.
type BootcBuildResult struct {
	PVCName       string
	RootUUID      string // root filesystem UUID, for the root= kernel arg
	KernelVersion string // kernel version shipped in the image, for kernelBoot paths
}

// BootcBuildDisk orchestrates the on-cluster bootc disk build pipeline.
// Progress and builder logs are written to progress (defaults to stderr),
// so callers like the web UI can capture them per-task.
func BootcBuildDisk(name, namespace, imageURI, sshPublicKey, diskSize string, progress io.Writer) (*BootcBuildResult, error) {
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
	pvc := GeneratePVC(pvcName, namespace, diskSize)
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
func generateBootcJob(name, namespace, imageURI, pvcName, sshKeyB64, diskSize string) string {
	sizeVal := strings.TrimSuffix(strings.TrimSuffix(diskSize, "Gi"), "G")
	if sizeVal == diskSize {
		sizeVal = strings.TrimSuffix(strings.TrimSuffix(diskSize, "Mi"), "M")
	}
	if sizeVal == diskSize {
		sizeVal = "50"
	}

	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app: tailvm-bootc-builder
spec:
  backoffLimit: 1
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels:
        app: tailvm-bootc-builder
    spec:
      restartPolicy: Never
      initContainers:
        - name: create-disk
          image: alpine:latest
          command: ["sh", "-c", "rm -f /output/disk.raw && truncate -s %sG /output/disk.raw"]
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

              LOOP=$(losetup -f --show /output/disk.raw)
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
`, name, namespace, sizeVal, imageURI, name, imageURI, sshKeyB64, pvcName)
}

// waitForBootcJob waits for a Job to complete, streaming builder logs to
// stderr, and returns the KEY=VALUE variables echoed by the build script.
func waitForBootcJob(name, namespace string, progress io.Writer) (map[string]string, error) {
	var podName string
	for i := 0; i < 60; i++ {
		out, err := exec.Command("kubectl", "get", "pod", "-n", namespace,
			"-l", "job-name="+name,
			"-o", "jsonpath={.items[0].metadata.name}").Output()
		if err == nil && len(out) > 0 {
			podName = strings.TrimSpace(string(out))
			break
		}
		time.Sleep(2 * time.Second)
	}
	if podName == "" {
		return nil, fmt.Errorf("builder pod not found after 2 minutes")
	}

	// Wait for builder container to start (it pulls the bootc image, which can be large)
	for i := 0; i < 150; i++ {
		ready, _ := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
			"-o", "jsonpath={.status.containerStatuses[?(@.name==\"builder\")].started}").Output()
		if strings.TrimSpace(string(ready)) == "true" {
			break
		}
		reason, _ := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
			"-o", "jsonpath={.status.containerStatuses[?(@.name==\"builder\")].state.waiting.reason}").Output()
		if reason := strings.TrimSpace(string(reason)); reason != "" {
			fmt.Fprintf(progress, "  Pod initializing: builder (%s)\n", reason)
		}
		time.Sleep(2 * time.Second)
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

// GenerateBootcVM creates a KubeVirt VirtualMachine manifest for a bootc-built disk.
// Uses kernel boot (vmlinuz+initrd pulled from the bootc image at the detected
// kernel version) since GRUB can't install on loopback devices with this
// cluster's kernel.
func GenerateBootcVM(name, namespace, pvcName, imageURI, rootUUID, kernelVersion, mem string, cpu int, node string) map[string]any {
	if mem == "" {
		mem = "4G"
	}
	if cpu == 0 {
		cpu = 2
	}

	memMib := parseMem(mem)
	kernelArgs := fmt.Sprintf("root=UUID=%s ro console=ttyS0", rootUUID)

	spec := map[string]any{
		"running": false,
		"template": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"kubevirt.io/vm": name},
			},
			"spec": map[string]any{
				"domain": map[string]any{
					"cpu":    map[string]any{"cores": cpu},
					"memory": map[string]any{"guest": fmt.Sprintf("%dMi", memMib)},
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
					},
				},
				"volumes": []map[string]any{
					{
						"name":                  "rootdisk",
						"persistentVolumeClaim": map[string]any{"claimName": pvcName},
					},
				},
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
			"labels":    map[string]any{"tailvm": name},
		},
		"spec": spec,
	}
}
