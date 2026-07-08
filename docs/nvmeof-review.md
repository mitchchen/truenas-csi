# NVMe-oF Implementation Review

Branch: `nvmeof`  
Date: 2026-06-09

---

## Security Findings

### CONFIRMED bugs requiring fix before production

#### 1. DH-CHAP secrets exposed in `/proc/<pid>/cmdline`
**File:** `pkg/driver/nvmeof.go:204`

`nvme connect` receives `--dhchap-secret` and `--dhchap-ctrl-secret` as CLI arguments. Any process with access to `/proc` (node-exporter, a compromised DaemonSet, host-PID-namespace containers) can read them during the subprocess window.

```go
// current — plaintext in process args
args = append(args, "--dhchap-secret", config.DHCHAPKey)
args = append(args, "--dhchap-ctrl-secret", config.DHCHAPCtrlKey)
```

Fix: write secret to a 0600 temp file, pass `--dhchap-secret=@/path/to/file`, delete after connect.

#### 2. `NodeStageVolumeRequest` logged unredacted at trace level
**File:** `pkg/driver/driver.go:596`

`sanitizeRequest` only handles `*csi.CreateVolumeRequest`. All other types fall through to `default: return req`. `NodeStageVolumeRequest` carries `VolumeContext` with `nvmeof.dhchapKey` and `nvmeof.dhchapCtrlKey` as plaintext strings. Enabling `--v=5` writes them to the node log and any downstream aggregator.

Fix: add a `*csi.NodeStageVolumeRequest` case to `sanitizeRequest` that redacts `VolumeContext` values whose keys contain `dhchap`, `secret`, `password`, or `key`.

---

## All Review Findings (ranked by severity)

| # | File | Line | Summary |
|---|------|------|---------|
| 1 | `pkg/driver/nvmeof.go` | 204 | DH-CHAP secrets in CLI args → visible in `/proc/<pid>/cmdline` |
| 2 | `pkg/driver/driver.go` | 596 | `NodeStageVolumeRequest` logged raw at trace; DH-CHAP keys exposed |
| 3 | `pkg/driver/driver.go` | 1103 | `SubNQN` is `""` when `ns.Subsys == nil` → Stage rejects every mount |
| 4 | `pkg/driver/controller.go` | 604 | NQN name cap is 200 bytes, ignores variable `baseNQN` length; 223-byte spec limit can be exceeded |
| 5 | `pkg/driver/nvmeof.go` | 443 | `Publish` swallows real `IsLikelyNotMountPoint` I/O errors |
| 6 | `pkg/driver/driver.go` | 874 | `nvmeofPortID` read/write unsynchronized — data race under `-race` |
| 7 | `pkg/client/nvme.go` | 264 | Client-side filter for namespace/port/host queries; `nil` nested objects silently drop records, leaking TrueNAS resources |
| 8 | `pkg/driver/nvmeof.go` | 523 | `Expand` swallows namespace rescan failures; returns success with wrong capacity |
| 9 | `pkg/driver/controller.go` | 912 | Shared DH-CHAP host GC skipped on transient delete error; host objects leak |
| 10 | `pkg/driver/controller.go` | 613 | `hostNQN` not validated client-side before reaching TrueNAS API |

### Finding 3 detail — SubNQN empty when Subsys nil

`NVMeNamespace.Subsys` is `json:"subsys,omitempty"` (nullable). If TrueNAS returns a namespace response without the nested subsys, the `if ns.Subsys != nil` guard at `driver.go:1103` is skipped, `volInfo.NVMeSubNQN` stays `""`, and `publishContext[PublishContextNVMeSubNQN]` is set to `""`. `NodeStageVolume`'s `Stage()` check (`config.SubNQN == ""`) then rejects every mount attempt for that volume.

### Finding 4 detail — NQN length cap

TrueNAS generates `subnqn` as `baseNQN + ":" + name`. The NVM Express spec limits NQN to 223 bytes. `maxNVMeSubsysNameLength = 200` does not subtract the actual `baseNQN` length. With a 54-byte baseNQN the combined subnqn can reach 255 bytes, causing TrueNAS to reject `CreateNVMeSubsystem` with a validation error.

Fix: `min(200, 222-len(d.nvmeBaseNQN))` — `nvmeBaseNQN` is already stored at startup.

---

## Completeness Gaps

### 1. DH-CHAP credentials in StorageClass — no Kubernetes Secret support

`nvmeof.dhchapKey` and `nvmeof.dhchapCtrlKey` are StorageClass parameters. StorageClass objects are unencrypted in etcd and readable by anyone with `kubectl get storageclass -o yaml`. CSI provides `nodeStageSecretRef` / `nodePublishSecretRef` on PVCs for exactly this case.

iSCSI CHAP has the same limitation in this driver, but NVMe DH-CHAP is the primary access-control mechanism for NVMe-oF — the exposure is more significant.

### 2. No RDMA transport or ANA support

`NVMeTransportTCP = "TCP"` is the only transport constant. `NVMeGlobalConfig.ANA` and `.RDMA` fields are modeled in the client but never used. Acceptable for v1 but limits HA deployments. See [NVMe over RoCE changes](#nvme-over-roce-required-changes) below.

### 3. `nvme connect` tuning limited to `ctrl-loss-tmo`

Only `--ctrl-loss-tmo 1800` is set. `--reconnect-delay`, `--keep-alive-tmo`, and `--nr-io-queues` are not configurable. Consider exposing at least `nr-io-queues` as a StorageClass parameter for multi-core workloads.

### 4. NVMe-oF service enablement not checked at startup

`NewDriver` validates the pool and resolves the iSCSI portal at startup. For NVMe-oF, port resolution is non-fatal (deferred). If the nvmet service is disabled on TrueNAS, the first `CreateVolume` fails with a raw API error. `GetNVMeGlobalConfig` is already called for `nvmeBaseNQN`; adding a check for `gc.Kernel == true` (or similar) would give operators a clear startup error.

### 5. Transport value mismatch

`defaultNVMeOFTransport = "tcp"` (lowercase) is written into publish context and passed to `nvme connect -t tcp`. `NVMeTransportTCP = "TCP"` (uppercase) is sent to TrueNAS `CreateNVMePort`. `nvme-cli` accepts lowercase so this works, but the inconsistency is a latent bug if any code path compares these values.

### 6. `NVMeDeviceTypeFile` dead code

`NVMeDeviceTypeFile = "FILE"` is defined in `pkg/client/nvme.go` but never used. All volumes use `NVMeDeviceTypeZVOL`. Either remove it or wire it up.

### 7. Block-volume NodeStageVolume idempotency check is a no-op

`node.go:159` calls `IsLikelyNotMountPoint(req.StagingTargetPath)` and early-returns if already mounted. For block volumes the NVMe Stage handler never mounts the staging path, so this check always falls through and calls `handler.Stage()` again. `Stage()` is itself idempotent (`findDevice` first), so this is safe but wasteful.

---

## Additional Findings

### MULTI_NODE access modes advertised for block volumes — data corruption risk

`driver.go` advertises the same `volumeCaps` for all protocols, including `MULTI_NODE_MULTI_WRITER` and `MULTI_NODE_SINGLE_WRITER`. These are appropriate for NFS but not for NVMe-oF or iSCSI raw block.

If a user creates a NVMe-oF PVC with `accessModes: [ReadWriteMany]`, Kubernetes accepts it (the driver claims support). Two pods on different nodes both trigger `NodeStageVolume`. Both call `nvme connect` to the same subsystem. Both nodes get the same block device. Without a cluster-aware filesystem (GFS2, OCFS2), concurrent writes corrupt data — nothing in the driver or Kubernetes prevents this.

**Fix:** filter advertised access modes by protocol. Block-backed protocols (NVMe-oF, iSCSI) should only advertise `SINGLE_NODE_*` and `MULTI_NODE_READER_ONLY`.

### `allow_any_host=true` is the default

When no `nvmeof.hostNQN` is configured, `allowAnyHost` is `true`:

```go
allowAnyHost := hostNQN == ""
```

Any host that can reach port 4420 on TrueNAS can connect to any subsystem and read/write it without authentication. For clusters where the storage network is shared with untrusted hosts, this is a significant exposure.

Network segmentation (dedicated storage VLAN, firewall port 4420 to cluster nodes only) is the only mitigation in the current design. The documentation should call this out explicitly.

### Binary file in repo tree

`openshift-client-linux.tar.gz` is sitting untracked in the working directory root. Do not commit it — binary tarballs bloat `git clone`, break diff tooling, and carry their own CVE surface. Move it out of the repo or add to `.gitignore`.

See [nvmeof-roce.md](nvmeof-roce.md) for NVMe over RoCE planning notes.
