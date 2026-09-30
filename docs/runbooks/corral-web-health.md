# Corral Web Service Health and Troubleshooting Runbook

This runbook helps an operator to diagnose and resolve availability, probe failure, and metrics issues on `corral-web` service instances.

## 1. Architecture Overview

`corral-web` is the Proxmox-style web UI for Corral. It is also the unified management daemon for multiple backends. In Kubernetes deployments (see `deploy/corral-web.yaml`), it runs as a deployment in the `corral` namespace exposed via the Tailscale Ingress operator.

Key Endpoints:
- `GET /healthz`: Liveness probe. Returns HTTP `200 OK` (`ok\n`) when the server process and HTTP multiplexer are alive.
- `GET /readyz`: Readiness probe. Returns HTTP `200 OK` (`{"status":"ready"}`) when the server has initialized internal storage and core dependencies. Returns `503 Service Unavailable` if unready.
- `GET /metrics`: Prometheus metrics exposition. Exposes collector age, backend reachability, and instance inventory status.
- `GET /api/doctor`: On-demand diagnostic checks that cover cluster capabilities, KubeVirt, CDI, QEMU, and virtualization extensions.

---

## 2. Common Alert Conditions & Symptoms

### Symptom A: Pod Failing Readiness Probe (`/readyz` returning 503)

**Root Causes:**
1. State directory unmounted, corrupted, or non-writable (`~/.local/share/corral/registry.json`).
2. Lock contention or startup hang during registry initialization.

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
