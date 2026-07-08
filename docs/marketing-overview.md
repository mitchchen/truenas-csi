# TrueNAS CSI Driver — Background for Marketing

## What it is

A Container Storage Interface (CSI) driver that connects TrueNAS storage to Kubernetes and Red Hat OpenShift clusters. CSI is the standard plugin interface for storage in Kubernetes — having a CSI driver means TrueNAS is a fully supported, first-class storage provider in those environments.

**The TrueNAS CSI Driver is Red Hat OpenShift Certified**, listed in the Red Hat Ecosystem Catalog at: https://catalog.redhat.com/en/software/container-stacks/detail/6984e321fea5c2d518c956f6

This certification means the driver has been validated by Red Hat to work correctly on OpenShift, meets Red Hat's security and operational standards, and is built on Red Hat Universal Base Image (UBI). Customers can install it with confidence directly from OperatorHub inside their OpenShift console.

## What problem it solves

Container workloads need persistent storage, but Kubernetes doesn't manage storage natively — it relies on external drivers. Without this driver, connecting TrueNAS to a Kubernetes cluster required manual volume setup and ongoing administrative effort outside of Kubernetes tooling. This driver automates that entirely: storage is requested, created, resized, snapshotted, and deleted automatically in response to what applications declare they need.

## What it supports

### Storage protocols

- **NFS** — shared storage where multiple application instances can read and write simultaneously
- **iSCSI** — block-level storage suited to databases and high-performance workloads; supports both filesystem mount and raw block device access modes
- **NVMe over Fabrics (NVMe-oF/TCP)** — block-level storage using the NVMe protocol over standard TCP/IP networks; lower latency than iSCSI for high-performance database and analytics workloads; requires TrueNAS 25.10+ with the NVMe-oF target service enabled

### Volume lifecycle

- Dynamic provisioning and deletion
- Online volume resizing (no downtime), including filesystem expansion for iSCSI and NVMe-oF
- Cloning — create a new volume directly from an existing volume or snapshot
- Snapshots via standard Kubernetes CSI snapshot APIs
- Automated snapshot schedules configured per StorageClass (cron-based, with configurable retention)
- Thin provisioning for iSCSI and NVMe-oF volumes (sparse ZVOLs)
- Volume health reporting — the driver can report volume condition (e.g., space exhaustion) back to Kubernetes
- Volume listing and available capacity reporting

### ZFS storage features exposed to Kubernetes users

- Compression: LZ4, ZSTD (including individual compression levels 1–9), GZIP (including levels 1 and 9), ZLE, LZJB
- Configurable ZFS sync mode (standard, always, disabled) — allows trading durability for write performance
- Configurable record size for NFS, block size for iSCSI and NVMe-oF
- Access time tracking can be disabled for performance
- Dataset-level encryption: AES-256-GCM (default), AES-128-CCM, and other AES variants; key can be auto-generated, user-supplied, or passphrase-derived with configurable PBKDF2 iterations
- Volumes can be organized into nested paths within a ZFS pool

### Security and access control

- iSCSI CHAP authentication (single and mutual)
- iSCSI initiator allowlisting — restrict which hosts can connect to a target
- NVMe-oF DH-CHAP authentication — cryptographically stronger than CHAP; uses Diffie-Hellman key exchange so the shared secret is never transmitted on the wire
- NVMe-oF host NQN allowlisting — restrict which initiator hosts can connect to a subsystem
- NFS host and network allowlisting
- NFS user/group mapping

## Operational design

The driver reconstructs volume metadata by querying TrueNAS directly, rather than maintaining a local database. This means it is resilient to pod restarts with no risk of state loss or data inconsistency.

NFS server address, iSCSI portal, and NVMe-oF portal can be derived automatically from the TrueNAS URL, minimizing required configuration.

## Platform support

- Standard Kubernetes 1.26+
- Red Hat OpenShift 4.20+ — certified and available via OperatorHub, installable through the OpenShift web console with a dedicated Kubernetes Operator managing the driver lifecycle

## How TrueNAS is configured

A single configuration point: TrueNAS URL, API key, and ZFS pool name. The driver communicates with TrueNAS 25.10.0+ through its WebSocket API. After initial setup, all storage operations are driven by Kubernetes declarations — no ongoing storage administration required.

## Who uses it

Kubernetes and OpenShift users who have TrueNAS storage and want to use it as persistent storage for containerized applications without manual volume management.

---

> **Note for marketing:** the Red Hat Ecosystem Catalog page linked above is the canonical place to point OpenShift customers — it provides Red Hat's own certification badge and one-click install path. Worth making that link prominent in the blog post.
