# NVMe over RoCE: Planning Notes

---

## Driver Code Changes

### `pkg/client/nvme.go`

Add constant:
```go
NVMeTransportRDMA = "RDMA"
```

`CreateNVMePort` — add `transport string` parameter; replace hardcoded `NVMeTransportTCP`:
```go
// line 296
AddrTrType: NVMeTransportTCP,   // → transport
```

`GetNVMePortByAddr` — add `transport string` parameter; replace hardcoded filter:
```go
// line 315
if ports[i].AddrTrType == NVMeTransportTCP {   // → == transport
```

### `pkg/driver/driver.go`

Add to `DriverConfig`:
```go
NVMeOFTransport string   // "TCP" or "RDMA"; defaults to "TCP"
```

Add `nvmeofTransport string` to `Driver` struct.

`resolveOrCreateNVMePort` — add `transport string` param; thread into both `GetNVMePortByAddr` and `CreateNVMePort`.

`NVMeOFPortID` — pass `d.nvmeofTransport` into `resolveOrCreateNVMePort`.

Auto-derive logic (line 345) — TCP auto-derive from URL stays. RDMA requires explicit `TRUENAS_NVMEOF_TRANSPORT=RDMA`.

### `pkg/driver/nvmeof.go`

`loadKernelModules` — select module by transport:
```go
transportMod := "nvme_tcp"
if h.transport == client.NVMeTransportRDMA {
    transportMod = "nvme_rdma"
}
for _, mod := range []string{transportMod, "nvme_fabrics"} {
```

`NVMeOFHandler` needs a `transport string` field set in `NewNVMeOFHandler`.

`nvme connect -t` already reads `config.Transport` — no change needed there.

### `pkg/driver/controller.go`

`PublishContextNVMeTransport` is hardcoded to `defaultNVMeOFTransport` in three places:
- `createNVMeOFVolume` (via volInfo)
- `reconstructVolumeFromTrueNAS` (line 1110)
- `ControllerPublishVolume` fallback (line 1838)

All three should read from `d.nvmeofTransport` instead.

### `deploy/truenas-csi-driver.yaml`

Init container command — add RDMA module loading:
```sh
modprobe nvme_rdma 2>/dev/null; modprobe nvme_fabrics 2>/dev/null
```

`nvme_rdma` pulls `ib_core` and `rdma_cm` as dependencies automatically.

No new volume mounts needed — the DaemonSet already mounts the full host `/dev`, which includes `/dev/infiniband/*`.

---

## Kubernetes Environment

RoCE support requires non-trivial Kubernetes-side changes. The existing DaemonSet is not sufficient.

### What the current DaemonSet already has

- `privileged: true` — necessary but not sufficient
- `hostNetwork: true` — required, already present
- Full host `/dev` mount — exposes `/dev/infiniband/*` device files when present

### What is missing

**1. Unlimited memory locking (MEMLOCK)**

Container runtimes (containerd, CRI-O) impose `RLIMIT_MEMLOCK` limits on containers independently of `privileged: true`. RDMA operations require locking physical memory pages for DMA. Without explicit override, the container will hit this limit at runtime. The DaemonSet needs:

```yaml
securityContext:
  privileged: true
resources:
  limits:
    memory: ...
securityContext:
  runAsUser: 0
```

And at the container runtime level, the MEMLOCK ulimit must be set to unlimited. How to configure this depends on the container runtime:
- containerd: `default_ulimits` in `/etc/containerd/config.toml`
- Docker: `--default-ulimit memlock=-1:-1` on the daemon
- CRI-O: `default_ulimits` in `/etc/crio/crio.conf`

There is currently no mechanism to express this in the Kubernetes DaemonSet spec itself (no `ulimits` field in the container spec). It requires node-level runtime configuration.

**2. NVIDIA/Mellanox Network Operator (or equivalent)**

The Network Operator is required for production deployments because:

- It installs MLNX_OFED drivers on cluster nodes. The in-tree `mlx5_ib` kernel module supports basic RoCE but production deployments typically require out-of-tree OFED for performance features and firmware alignment.
- It deploys the RDMA device plugin, which provides proper Kubernetes resource accounting for RDMA hardware — allocation, lifecycle management, and auditability.
- It manages SR-IOV VF provisioning for RDMA.

Using the raw hostPath `/dev` mount bypasses all of this. While it technically exposes the device files, it is not a supportable production approach.

With the Network Operator in place, the DaemonSet should request RDMA resources via the device plugin rather than relying on the full `/dev` mount:

```yaml
resources:
  limits:
    rdma/hca_shared_devices_a: 1   # or equivalent resource name
```

This also opens the path to eventually removing the full `/dev` hostPath mount (security improvement).

**3. Pod Security Admission / OpenShift SCC**

`privileged: true` with MEMLOCK unlimited may require explicit namespace-level PSA configuration or a custom OpenShift SCC beyond what iSCSI/NVMe-TCP currently needs. Validate against the cluster's security policy.

### What does NOT change

| Item | Reason |
|------|--------|
| ServiceAccount RBAC | Controls K8s API only; no change for device access |
| Workload pod permissions | Workload sees `/dev/nvmeXnY` as a normal block device; no RDMA exposure |
| Controller Deployment | No node-side code; unaffected |

### Technical note on kernel vs user-space RDMA

`nvme connect -t rdma` sends a write to `/dev/nvme-fabrics` and the kernel `nvme_rdma` module handles the RDMA connection entirely in kernel space via `rdma_cm`. The container running nvme-cli does not technically need `/dev/infiniband/uverbsN` or user-space memory locking for that specific operation. However:

- This is a narrow guarantee that breaks as soon as any diagnostic tooling, verbs-based health checks, or additional RDMA operations are added
- The MEMLOCK and device plugin requirements remain valid for the broader operational picture
- Depending on the narrow kernel-path behavior is not a sound basis for a production deployment plan

---

## Changes Required Outside Kubernetes

These are OS/hardware/network concerns.

**Cluster nodes:**
- RDMA-capable NIC (Mellanox ConnectX-4+, Broadcom BCM57500, Marvell FastLinQ 41000, etc.)
- Vendor RDMA driver (`mlx5_ib`, `bnxt_re`, etc.) — loaded by udev at boot when hardware is detected; the DaemonSet init container does not need to load it
- `modinfo nvme_rdma` should succeed on all worker nodes before deploying

**TrueNAS:**
- RDMA-capable NIC on the storage host
- `NVMeGlobalConfig.RDMA` must be `true` (verifiable via the existing `GetNVMeGlobalConfig` call at startup)

**Network fabric:**
- RoCE v2 (UDP encapsulation) is strongly preferred — routes over L3, no lossless fabric requirement
- RoCE v1 requires a lossless Ethernet fabric (PFC + ECN configured on switches) — switch configuration is outside Kubernetes entirely
- Dedicated storage VLAN is recommended regardless (also addresses the `allow_any_host` exposure noted in the review)

**Soft-RoCE (testing without hardware):**
```sh
modprobe rdma_rxe
rdma link add rxe0 type rxe netdev eth0
```
The `rdma link add` step requires knowing which physical interface to bind and cannot be automated inside the DaemonSet init container safely. Use a node bootstrap script (cloud-init, Ansible) instead.
