# TrueNAS CSI Driver — Best Practices

Version: 1.1.x  
TrueNAS minimum: 25.10.0  
Kubernetes minimum: 1.26

---

## Overview

The TrueNAS CSI driver connects TrueNAS storage to Kubernetes and Red Hat OpenShift clusters via the Container Storage Interface (CSI) standard. It enables fully automated, lifecycle-managed persistent storage: volumes are provisioned, resized, cloned, snapshotted, and deleted in response to Kubernetes declarations, with no manual storage administration required after initial setup.

The driver communicates with TrueNAS exclusively through the TrueNAS WebSocket API. It maintains no local state — all volume metadata is reconstructed by querying TrueNAS directly, which makes it resilient to pod restarts with no risk of state divergence.

**Supported storage protocols:**
- **NFS** — shared filesystem volumes (`ReadWriteMany`), backed by ZFS datasets
- **iSCSI** — block volumes (`ReadWriteOnce`), backed by ZFS zvols
- **NVMe-oF/TCP** — block volumes (`ReadWriteOnce`), backed by ZFS zvols; requires TrueNAS 25.10+ with the nvmet service enabled

**Supported volume operations:** dynamic provisioning, online expansion, cloning, CSI snapshots, automated TrueNAS-managed snapshot schedules, thin provisioning, volume health reporting, and available capacity reporting.

**ZFS features exposed to Kubernetes:** per-volume compression algorithm, sync mode, record/block size, dataset-level AES encryption with per-volume key generation, dataset path organization within a pool, and arbitrary ZFS properties via `zfs.*` StorageClass parameters.

The driver is Red Hat OpenShift Certified and available via OperatorHub.

---

## 1. Protocol Selection

Choose the protocol based on access pattern and workload type.

| Protocol | Access Mode | Workload fit |
|----------|-------------|--------------|
| NFS | ReadWriteMany | Shared filesystems, ML training data, CMS media, CI artifact caches |
| iSCSI | ReadWriteOnce | Databases, message queues, high-throughput block I/O |
| NVMe-oF/TCP | ReadWriteOnce | Databases and block workloads requiring lower latency than iSCSI |

**Rule: never use NFS for databases.** ZFS NFS shares do not provide POSIX advisory locking guarantees sufficient for transactional engines (PostgreSQL, MySQL, etcd). Use iSCSI or NVMe-oF with a filesystem (ext4, XFS) or raw block mode.

**Rule: only use `ReadWriteMany` with NFS.** iSCSI and NVMe-oF provide no cluster-aware block coordination. Two nodes writing the same block device without a cluster filesystem (GFS2, OCFS2) cause silent data corruption. See [Access Modes](#5-access-modes).

**NVMe-oF availability:** NVMe-oF/TCP requires TrueNAS 25.10+ with the nvmet service enabled (System Settings → Services → NVMe-oF) and `nvme_tcp`/`nvme_fabrics` kernel modules on every worker node. The DaemonSet init container loads these automatically.

---

## 2. TrueNAS Connection

### Use TLS

Always connect over `wss://` (WebSocket TLS). Plain `ws://` sends the API key in cleartext.

```yaml
truenasURL: "wss://storage.example.com/api/current"
truenasInsecure: "false"
```

Set `truenasInsecure: "true"` only in lab environments with self-signed certificates. In production, provision a certificate from your internal CA or a public CA and leave it `"false"`.

### Use an API key, not a password

TrueNAS API keys are scoped to a single service. Create a dedicated key for the CSI driver with the minimum required permissions. Store it in the Kubernetes Secret — never in a ConfigMap.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: truenas-api-credentials
  namespace: truenas-csi
type: Opaque
stringData:
  api-key: "1-<key>"
```

The Secret is referenced by the DaemonSet and Deployment via `secretKeyRef`. Do not embed the key value in the ConfigMap or in the image.

### Dedicated service account on TrueNAS

If TrueNAS supports fine-grained API key scoping in your version, restrict the key to the storage and sharing APIs only. The driver does not require console access, user management, or system settings APIs.

---

## 3. Network Architecture

### Storage network isolation

Put TrueNAS and Kubernetes worker nodes on a dedicated storage VLAN. This matters more than it may appear:

- NVMe-oF: without a separate storage network, `allow_any_host` (the default when no `nvmeof.hostNQN` is configured) means any host that can reach port 4420 can connect to any subsystem and read or write it. A dedicated storage VLAN is the only effective mitigation in the current driver.
- iSCSI: `iscsi.initiators` restricts access by initiator IQN, but network isolation prevents unauthenticated connect attempts from reaching the service at all.
- NFS: `nfs.hosts` / `nfs.networks` filter by IP. Enforce at the network layer too.

### DNS vs IP for portals

Use IP addresses for `nfsServer`, `iscsiPortal`, and `nvmeofPortal` rather than hostnames. DNS lookup failures at mount time cause node pods to crash-loop, and DNS is often slow or unavailable during early node boot. If you use DNS, ensure it is available before the CSI DaemonSet starts.

### Portal auto-derivation

If `iscsiPortal` and `nvmeofPortal` are omitted from the ConfigMap, the driver derives the host from `truenasURL`. This works in single-interface deployments. In multi-homed TrueNAS systems where storage traffic should go through a dedicated NIC, set the portal addresses explicitly.

---

## 4. StorageClass Design

### One StorageClass per workload tier

Design StorageClasses to match workload SLAs, not just protocols. A flat "truenas-iscsi" class that all workloads share makes it impossible to tune compression, block size, or sync mode per tier. Suggested starting tiers:

| Class | Protocol | Compression | Sync | Use case |
|-------|----------|-------------|------|----------|
| `truenas-nfs-standard` | NFS | LZ4 | standard | General shared storage |
| `truenas-block-db` | iSCSI or NVMe-oF | LZ4 | standard | Relational databases |
| `truenas-block-fast` | iSCSI or NVMe-oF | off | disabled | Write-heavy, already-compressed data |
| `truenas-block-safe` | iSCSI or NVMe-oF | LZ4 | always | Anything requiring strict write ordering |

### Reclaim policy

Default `reclaimPolicy: Delete` is appropriate for ephemeral workloads. For persistent data:

```yaml
reclaimPolicy: Retain
```

With `Retain`, the ZFS dataset survives PVC deletion and must be manually freed. This prevents accidental data loss and is the right default for stateful applications (databases, message brokers). Set up a monitoring alert for Released PVs to avoid orphaned storage accumulating on TrueNAS.

### Volume binding mode

`volumeBindingMode: WaitForFirstConsumer` is preferred when worker nodes have topology constraints (zone or node affinity). `Immediate` provisions the volume at PVC creation time before a pod is scheduled, which can create volumes in the wrong zone in multi-zone clusters. In single-zone or single-site deployments, `Immediate` is fine.

### ZFS parameters

**Compression:** `LZ4` is the right default — near-zero CPU cost, 1.5–3x compression on typical application data. Use `off` only for already-compressed data (video, pre-compressed backups, encrypted content). ZSTD offers better ratios at moderate CPU cost; use `ZSTD-3` or `ZSTD-6` for cold storage tiers.

**Sync:** `standard` (default) honors fsync. `disabled` gives the highest write throughput by buffering writes and not waiting for ZIL. Use `disabled` only when the application manages its own durability (e.g., Kafka with replication factor ≥ 3) or data loss on crash is acceptable. `always` forces every write to the ZIL before acknowledging — use for financial or audit log workloads.

**Block size (iSCSI and NVMe-oF):**
- ZVOL `volblocksize`: `16K` is a good default for mixed workloads. Databases with 8K page size (PostgreSQL default) benefit from `8K`. Large sequential workloads (analytics, backups) can use `64K` or `128K`.
- iSCSI logical block size (`iscsi.blocksize`): `4096` is appropriate for all modern systems. `512` is for legacy compatibility only.

**Dataset path:** Use `datasetPath` to organize volumes within a pool. Example: a pool named `tank` with `datasetPath: kubernetes/production` places all volumes under `tank/kubernetes/production/pvc-*`. This makes TrueNAS dataset views navigable and allows per-environment ZFS properties.

```yaml
parameters:
  pool: "tank"
  datasetPath: "kubernetes/production"
```

---

## 5. Access Modes

Block-backed protocols (iSCSI, NVMe-oF) support only these access modes safely:

| Mode | Protocol | Notes |
|------|----------|-------|
| `ReadWriteOnce` | iSCSI, NVMe-oF, NFS | Single writer |
| `ReadOnlyMany` | NFS | Read-only across pods |
| `ReadWriteMany` | NFS only | Multiple concurrent writers — NFS with cluster-safe data only |

**Do not request `ReadWriteMany` for iSCSI or NVMe-oF PVCs.** The driver currently advertises these modes as supported to satisfy CSI validation requirements. Kubernetes will accept the PVC and multiple nodes will connect to the same block device simultaneously, causing data corruption if no cluster filesystem is present. This is a known gap in the driver that will be tightened in a future release.

---

## 6. Encryption

### When to use ZFS dataset encryption

Use ZFS dataset encryption when:
- TrueNAS is co-located in a shared or untrusted physical environment
- Regulatory requirements demand at-rest encryption (HIPAA, PCI-DSS)
- Decommissioning storage requires cryptographic erasure

ZFS encryption is transparent to the CSI driver and Kubernetes. Performance impact with AES-256-GCM is minimal on modern CPUs with AES-NI.

### How key management works

The driver supports three key source modes, controlled by `parseEncryptionOptions` in `pkg/driver/controller.go`:

| Parameter | What happens | Key location |
|-----------|-------------|--------------|
| `encryption.generateKey: "true"` | TrueNAS generates a 256-bit key | TrueNAS key storage only — never transits the driver or Kubernetes |
| `encryption.passphrase: "<value>"` | TrueNAS derives a key from passphrase via PBKDF2 | Passphrase stored in StorageClass (etcd); TrueNAS holds derived key |
| `encryption.key: "<64-char hex>"` | Caller supplies raw 256-bit key | Key stored in StorageClass (etcd); also held by caller |

**If `encryption: "true"` is set with none of the three key sources specified, the driver defaults to `generateKey: true` automatically** (controller.go:2402–2403).

### Recommended: `generateKey`

`encryption.generateKey: "true"` is the right choice for almost all deployments. TrueNAS generates a unique key per dataset and stores it in its own key management system. The key material never appears in the driver, in Kubernetes API objects, or in etcd. Each volume gets a distinct key — compromise of one volume does not affect others.

```yaml
parameters:
  encryption: "true"
  encryption.generateKey: "true"
  encryption.algorithm: "AES-256-GCM"
```

Cryptographic erasure works correctly with this mode: when the dataset is deleted (PVC reclaim), TrueNAS deletes the key along with it. Data on the underlying ZFS vdev becomes permanently unrecoverable without the key.

### Passphrase mode

```yaml
  encryption.passphrase: "minimum-8-chars"
  encryption.pbkdf2iters: "350000"   # default; minimum enforced at 100000
```

The passphrase is PBKDF2-derived to an encryption key on TrueNAS. Two problems for production use:

1. The passphrase is stored in the StorageClass object, which is unencrypted in etcd and readable by anyone with `get storageclass` RBAC.
2. TrueNAS requires the passphrase to unlock encrypted datasets on reboot. Automated startup (e.g., after a power failure) is blocked until an operator manually enters the passphrase.

Suitable for air-gapped environments where operator-controlled unlock on reboot is desirable. Not appropriate for automated cloud or co-lo deployments.

### Explicit key mode

```yaml
  encryption.key: "0123456789abcdef..."   # 64 hex chars = 256 bits
```

The raw hex key is sent to TrueNAS and also stored in the StorageClass object (etcd). Use this only when integrating with an external KMS that supplies per-volume keys — and in that case, the KMS should supply a unique key per StorageClass, not a single shared key across all volumes. A shared key means all volumes encrypted with it stand or fall together.

---

## 7. Authentication

### iSCSI CHAP

CHAP (Challenge-Handshake Authentication Protocol) restricts which initiators can authenticate to a target. Without CHAP, any host on the storage network can attach the volume.

For production, use mutual CHAP (bidirectional):

```yaml
parameters:
  iscsi.chapUser: "initiator-username"
  iscsi.chapSecret: "initiator-password-min-12-chars"
  iscsi.chapPeerUser: "target-username"
  iscsi.chapPeerSecret: "target-password-min-12-chars"
```

Combine with `iscsi.initiators` to restrict by IQN:

```yaml
  iscsi.initiators: "iqn.1993-08.org.debian:node1,iqn.1993-08.org.debian:node2"
```

**Security note:** CHAP credentials stored in StorageClass parameters are unencrypted in etcd and readable by anyone with `get storageclasses` RBAC. For stricter isolation, use separate StorageClasses per namespace/team so CHAP credentials are scoped.

### NVMe-oF DH-CHAP

DH-CHAP (Diffie-Hellman CHAP) is the NVMe-oF authentication mechanism. It is stronger than iSCSI CHAP: uses Diffie-Hellman key exchange so the shared secret is never transmitted on the wire.

```yaml
parameters:
  nvmeof.hostNQN: "nqn.2014-08.org.nvmexpress:uuid:<uuid>"
  nvmeof.dhchapKey: "DHHC-1:00:<base64>"
  nvmeof.dhchapCtrlKey: "DHHC-1:00:<base64>"   # mutual auth
  nvmeof.dhchapHash: "SHA-384"
  nvmeof.dhchapDHGroup: "4096-BIT"
```

Without `nvmeof.hostNQN`, the subsystem is created with `allow_any_host = true` — any host reaching port 4420 can connect. This is acceptable only when the storage network is fully isolated.

**Security note:** Like iSCSI CHAP, DH-CHAP credentials in StorageClass are unencrypted in etcd. This is a known limitation. Do not store DH-CHAP keys in StorageClass objects if your threat model includes etcd access by internal adversaries. This will be addressed in a future release via `nodeStageSecretRef` support.

**Hash and DH group:** Prefer `SHA-384` or `SHA-512` over `SHA-256`. For the DH group, `4096-BIT` or larger is recommended for new deployments. Stronger groups add negligible overhead to the one-time key exchange.

---

## 8. Snapshots

### Prerequisites

The external snapshot controller and `VolumeSnapshotClass` must be deployed before creating any `VolumeSnapshot` objects. The CSI driver does not deploy these — they are cluster-level components. See the [Kubernetes CSI snapshot documentation](https://kubernetes-csi.github.io/docs/snapshot-restore-feature.html).

### Automated snapshot schedules

The driver supports TrueNAS-managed automated snapshot schedules via StorageClass parameters. These run on TrueNAS independently of Kubernetes and survive pod restarts:

```yaml
parameters:
  snapshot.schedule: "0 2 * * *"      # cron: nightly at 02:00
  snapshot.retention: "7"              # keep 7 snapshots
  snapshot.retentionUnit: "count"      # "count" or "days"
  snapshot.naming: "auto-%Y-%m-%d"    # TrueNAS snapshot name template
```

TrueNAS-managed snapshots are distinct from Kubernetes `VolumeSnapshot` objects. They do not appear in `kubectl get volumesnapshot` and cannot be used as PVC clone sources from within Kubernetes. Use them for operational backup/recovery; use `VolumeSnapshot` for application-level point-in-time copies.

### Clone vs snapshot for seeding

Cloning from an existing PVC (supported via `dataSource`) is more efficient than snapshot+restore when populating a new volume from a known-good baseline, because ZFS clone creation is instantaneous (copy-on-write). The clone is backed by the source dataset until data diverges, consuming no additional space until writes occur.

---

## 9. Logging and Observability

### Log levels

The driver uses structured logging via `klog`. The deployment default is `-v=2`. Levels:

| Level | Content | Use |
|-------|---------|-----|
| 0–2 | Errors, warnings, major lifecycle events | Production |
| 3–4 | Per-operation detail (volume create, attach) | Active debugging |
| 5+ | Full request/response, trace paths | Development only |

**Warning:** `-v=5` or higher logs `NodeStageVolumeRequest` contents, which includes `VolumeContext` fields. If DH-CHAP credentials are stored in StorageClass parameters, they appear in the log at trace level. Do not enable trace logging in production without first auditing what log aggregation systems downstream receive the output.

### Volume health

The driver reports volume health conditions back to Kubernetes via the `VolumeConditionAbnormal` event. Operators can surface these with:

```bash
kubectl describe pv <pv-name>
kubectl get events --field-selector reason=VolumeConditionAbnormal
```

### Key metrics to monitor

- Controller and node pod restart count (CrashLoopBackOff indicates persistent errors)
- `Released` PVs accumulating (orphaned datasets with `reclaimPolicy: Retain`)
- TrueNAS pool utilization — volumes are thin-provisioned; actual usage can diverge from requested capacity

---

## 10. Upgrade

### Driver upgrades

The driver is stateless — it reconstructs all volume metadata from TrueNAS on startup. Rolling out a new DaemonSet version does not require draining nodes or unmounting volumes. In-flight I/O to existing volumes continues through the upgrade.

Controller upgrades cause a brief window where new volume provisioning requests queue. Existing mounted volumes are unaffected.

### TrueNAS upgrades

The driver pins to a specific TrueNAS API version. Before upgrading TrueNAS, verify the driver version supports the target TrueNAS release. Mismatched versions typically manifest as `CreateVolume` failures, not data loss.

Do not upgrade TrueNAS while volumes are actively mounting or unmounting. Complete any in-progress CSI operations first.

---

## 11. OpenShift-specific Notes

The driver is Red Hat OpenShift Certified and available via OperatorHub. Install it through OperatorHub rather than applying the raw YAML — the Operator manages lifecycle, upgrade, and SCC configuration automatically.

If deploying via raw YAML on OpenShift, ensure:
- The `truenas-csi` namespace has the `privileged` SCC bound to the node ServiceAccount
- The node DaemonSet pod spec requests `privileged: true` — required for `hostPID`, `/dev` mounts, and `nsenter`-based iSCSI operations

See `docs/openshift/` for installation, cluster setup, and certification details.

---

## 12. Known Limitations

| Limitation | Workaround |
|-----------|------------|
| CHAP and DH-CHAP credentials in StorageClass are unencrypted in etcd | Use dedicated StorageClasses per team; restrict etcd access; planned: `nodeStageSecretRef` support |
| `ReadWriteMany` accepted for iSCSI/NVMe-oF PVCs (data corruption risk) | Never use RWX access mode for block protocols |
| NVMe-oF `allow_any_host` is the default when no `hostNQN` is configured | Use storage network isolation; configure `nvmeof.hostNQN` when supported |
| No RDMA/RoCE transport support (NVMe-oF is TCP-only) | Planned; see `docs/nvmeof-roce.md` |
| NVMe-oF DH-CHAP keys visible in `/proc/<pid>/cmdline` during connect | Planned fix: secret-file passthrough to nvme-cli |
| Automated snapshot schedules are TrueNAS-managed (not Kubernetes VolumeSnapshot) | Use both independently; TrueNAS snapshots for ops, VolumeSnapshot for app-level |
