# Multipath HA Design

Date: 2026-02-25

## Overview

Add multipath iSCSI support for TrueNAS HA systems. TrueNAS HA exposes two
controllers, each with its own iSCSI portal IP. To survive controller failover,
the driver must discover and maintain sessions to all configured portals, and
the Linux multipath daemon must be configured to recognize TrueNAS devices.

## Scope

Only iSCSI volumes are affected. NFS volumes are unchanged.

---

## Section 1: Config & Driver (`driver.go`, `cmd/main.go`)

`Driver.iscsiPortal string` → `Driver.iscsiPortals []string`
`DriverConfig.ISCSIPortal string` → `DriverConfig.ISCSIPortals []string`

- `ISCSIPortal() string` accessor kept, returns `iscsiPortals[0]` — used by
  controller for provisioning (no controller-side change needed).
- New `ISCSIPortals() []string` accessor added for node-side use.
- `cmd/main.go`: `TRUENAS_ISCSI_PORTAL` env var split on commas and trimmed
  into `config.ISCSIPortals`.
- Auto-derive from TrueNAS URL produces a single-element slice
  (`[]string{host + ":3260"}`).

---

## Section 2: iSCSI Handler (`iscsi.go`)

`ISCSIHandler` gains a `portals []string` field.

- `NewISCSIHandler(mounter, log, portals)` — portals injected at construction.
- `NewNodeServer` passes `driver.ISCSIPortals()` when constructing the handler.
- `buildConnector`: `TargetPortals` set to `h.portals` (all portals) instead
  of `[]string{config.TargetPortal}` (single portal).
- When `config.MultipathEnabled` is true, `Multipath: true` is set on the
  connector so `csi-lib-iscsi` returns the `/dev/dm-X` device.
- `config.PersistentSessions` is wired to the connector's session persistence
  flag so `iscsid` reconnects automatically after failover.
- `config.TargetPortal` (from publish context) is still parsed for logging and
  backward compat, but `buildConnector` uses `h.portals` for the actual
  connection.

---

## Section 3: Dynamic Portal ID Lookup (`storage.go`)

Problem: `CreateISCSITargetWithAuth` hardcodes `Portal: 1`, which breaks when
TrueNAS regenerates its iSCSI config and assigns a different database ID.

Changes:
- Add `methodISCSIPortalQuery = "iscsi.portal.query"` constant.
- Add `GetPortalID(ctx context.Context, portalAddr string) (int, error)`:
  queries `iscsi.portal.query`, strips port from `portalAddr`, matches the
  first portal whose `Listen` list contains that IP, returns its `ID`.
- `CreateISCSITargetWithAuth` gains a `portalAddr string` parameter. It calls
  `GetPortalID` internally to resolve the live DB ID before building the target
  group.
- `CreateISCSITarget` convenience wrapper updated to pass `portalAddr`.
- All `controller.go` call sites pass `driver.ISCSIPortal()` (i.e.
  `iscsiPortals[0]`) as `portalAddr`.

---

## Section 4: Deploy (`deploy/truenas-csi-driver.yaml`)

### ConfigMap

`iscsiPortal` value updated to comma-separated format:

```
iscsiPortal: "192.168.1.10:3260,192.168.1.11:3260"
```

Key name unchanged — no env var or volume mount changes required.

### Node DaemonSet postStart hook

Extended to append a TrueNAS device stanza to
`/host/etc/multipath/multipath.conf` if one is not already present. No new
volume mount is needed since `/host` is already mounted with `Bidirectional`
propagation.

Stanza added:

```
devices {
  device {
    vendor            "TrueNAS"
    product           "iSCSI Disk"
    path_grouping_policy group_by_prio
    prio              alua
    path_checker      tur
    hardware_handler  "1 alua"
    failback          immediate
    fast_io_fail_tmo  15
  }
}
```

---

## What Is Not Changed

- NFS handling — unaffected.
- Publish context schema — `targetPortal` key unchanged (primary portal only),
  node uses its own portals config instead of relying on this value for
  multi-portal login.
- Controller provisioning logic — uses `iscsiPortals[0]` as before.
- `csi-lib-iscsi` dependency — no version bump required; existing multi-portal
  and multipath support in the library is sufficient.
