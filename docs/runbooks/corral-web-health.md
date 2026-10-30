# Corral Web Service Health and Troubleshooting Runbook

This runbook helps an operator to diagnose and resolve availability, probe failure, and metrics issues on `corral-web` service instances.

## 1. Architecture Overview

`corral-web` is the Proxmox-style web UI for Corral. It is also the unified management daemon for multiple backends. In Kubernetes deployments (see `deploy/corral-web.yaml`), it runs as a deployment in the `corral` namespace. The Ingress of the Tailscale operator exposes it. Without the operator, the `corral-web-tailscale` proxy pod from `deploy/corral-web-tailscale-proxy.yaml` exposes it.

Key Endpoints:
- `GET /healthz`: Liveness probe. Returns HTTP `200 OK` (`ok\n`) when the server process and HTTP multiplexer are alive.
- `GET /readyz`: Readiness probe. `corral-web` returns HTTP `200 OK` (`{"status":"ready"}`) when the registry store exists and `registry.Store.Ping` confirms its state directory is writable on every request. `Ping` creates and removes a temp probe file; it does not touch `registry.json` itself. If either check fails, `corral-web` returns `503 Service Unavailable` with an `error` field that states the failure.
- `GET /metrics`: Prometheus metrics exposition. Exposes collector age, backend reachability, and instance inventory status.
- `GET /api/doctor`: On-demand diagnostic checks that cover cluster capabilities, KubeVirt, CDI, QEMU, and virtualization extensions.

---

## 2. Common Alert Conditions & Symptoms

### Symptom A: Pod Failing Readiness Probe (`/readyz` returning 503)

**Root Causes:**
1. State directory unmounted or non-writable (`~/.local/share/corral/registry.json`'s parent dir). A write probe in `/readyz` catches this on every call, not only at startup.
2. Lock contention or startup hang during registry initialization (`store` still nil).

The `error` field in the `/readyz` response body distinguishes the two cases. `"registry store not initialized"` means (2). `"registry store unwritable: ..."` means (1) and includes the OS error below it.

**Diagnostic Steps:**
1. Check pod status and events:
   ```bash
   kubectl describe pod -n corral -l app=corral-web
   ```
2. Check pod logs:
   ```bash
   kubectl logs -n corral -l app=corral-web --tail=100
   ```
3. Test the probe endpoint manually within the pod:
   ```bash
   kubectl exec -n corral -it deploy/corral-web -- curl -i http://127.0.0.1:8006/readyz
   ```

**Mitigation:**
1. If the file permissions of the registry store are incorrect, verify the mount permissions of the container filesystem.
2. If transiently hung, restart the pod:
   ```bash
   kubectl rollout restart deployment/corral-web -n corral
   ```

---

### Symptom B: Pod Failing Liveness Probe (`/healthz` failing)

**Root Causes:**
1. The process deadlocks, or a fatal panic makes it crash.
2. The out-of-memory (OOM) killer kills the pod.

**Diagnostic Steps:**
1. Check termination reason:
   ```bash
   kubectl get pod -n corral -l app=corral-web -o jsonpath='{.items[*].status.containerStatuses[*].lastState.terminated.reason}'
   ```
   A restart with exit code `255` and reason `Unknown`, finished at the node's boot time, is a node reboot, not a crash. Compare with `uptime -s` on the node. The previous container's log (`kubectl logs -n corral deploy/corral-web --previous`) ends without a panic in that case.
2. Compare CPU and memory usage with the limits:
   ```bash
   kubectl top pod -n corral -l app=corral-web
   ```

**Mitigation:**
1. If OOMKilled, adjust memory limits in `deploy/corral-web.yaml` (default limit: 256Mi).
2. Inspect log panics and check if any calls to upstream backends blocked without timeouts.

---

### Symptom C: `corral_backend_up == 0` or High `corral_collection_age_seconds`

**Root Causes:**
1. The cluster's API server is unreachable, a network partition occurred, or the kubeconfig context has a wrong configuration.
2. Target backend (KubeVirt/libvirt/Incus/Proxmox) endpoint down or authentication token expired.
3. The pod has no `KUBECONFIG`. The UI lists no VMs, `/api/doctor` says "no Kubernetes cluster answered", and `/api/cts` reports that the cluster is unreachable. kubectl skips its in-cluster config once Corral adds `--request-timeout`, so it dials `localhost:8080`. Check with `kubectl -n corral exec deploy/corral-web -- kubectl get ns default --request-timeout=5s`.

**Diagnostic Steps:**
1. Query the doctor diagnostic endpoint:
   ```bash
   kubectl exec -n corral -it deploy/corral-web -- curl -s http://127.0.0.1:8006/api/doctor
   ```
2. Check the Prometheus metrics endpoint:
   ```bash
   kubectl exec -n corral -it deploy/corral-web -- curl -s http://127.0.0.1:8006/metrics | grep corral_backend
   ```

**Mitigation:**
1. Run fixable doctor reconciliations via `POST /api/doctor/fix` or CLI `corral doctor --fix`.
2. Check network connectivity and Tailscale status on target peer/backend nodes.
3. For cause 3, re-apply `deploy/corral-web.yaml`. It ships the `corral-web-kubeconfig` ConfigMap and mounts it.

---

### Symptom D: `https://corral.<tailnet>.ts.net` does not answer

**Root Causes:**
1. The proxy never joined the tailnet: no auth key and nobody opened the login URL.
2. An old offline device holds `corral`, so the proxy joined as `corral-1`.
3. The first HTTPS request after a join waits for the certificate. It can time out once.

**Diagnostic Steps:**
1. Check the proxy (operator-less installs):
   ```bash
   kubectl -n corral get pod -l app=corral-web-tailscale
   kubectl -n corral logs deploy/corral-web-tailscale | grep -E 'To authenticate|login.tailscale.com|Startup complete'
   ```
2. Find the name the node got, from any tailnet machine:
   ```bash
   tailscale status | grep corral
   ```

**Mitigation:**
1. Open the logged login URL, or create the `corral-web-tailscale-auth` Secret from an auth key file and restart: `kubectl -n corral rollout restart deploy/corral-web-tailscale`.
2. Delete the stale offline `corral` device in the admin console (Machines). Then rename the new one to `corral`, or delete the state and join again: `kubectl -n corral delete secret corral-web-tailscale-state && kubectl -n corral rollout restart deploy/corral-web-tailscale`.
3. Retry after a minute. After that, the state Secret keeps the certificate.

---

### Updating the image

The Deployment tracks `ghcr.io/tuna-os/corral:latest` with `imagePullPolicy: Always`. After main publishes, run `kubectl -n corral rollout restart deploy/corral-web`. See [deployment modes](../deployment-modes.md#updating) to pin or roll back.
