# iSCSI RDMA (iSER) Support

This document describes what would be required to add iSER (iSCSI Extensions for RDMA) support to the TrueNAS CSI driver.

## Current State

The driver uses the `github.com/kubernetes-csi/csi-lib-iscsi` library, which wraps `iscsiadm` under the hood. `iscsiadm` supports iSER via an interface flag, but the driver currently always uses TCP and never sets the transport type on the connector.

## Prerequisites (outside the driver)

- Worker nodes need RDMA hardware (InfiniBand or RoCE NIC) and drivers
- The `isert` kernel module loaded on each worker node
- `open-iscsi` compiled with iSER support

## TrueNAS Side

TrueNAS would need an iSER portal configured separately from the TCP portal. The driver's current configuration holds only one `iscsiPortal` address; a distinct iSER portal address would be needed.

## Driver Changes Required

### 1. Configuration (`driver.go`)

Add an `ISCSITransport` field and an iSER portal address to `DriverConfig`, since the iSER portal will typically be a different IP/port than the TCP portal.

### 2. StorageClass parameter (`controller.go`)

Add an `iscsi.transport` parameter (valid values: `tcp`, `iser`) so users can select transport per StorageClass.

### 3. Publish context (`controller.go`)

Pass the transport type through `ControllerPublishVolume` so the node knows which transport to use when staging the volume.

### 4. Connector construction (`iscsi.go`)

This is the core driver change. The `iscsilib.Connector` struct has an `Interface` field; setting it to `"iser"` causes `iscsiadm` to use the iSER transport. Currently `buildConnector` never sets this field.

### 5. Validation

- Add `iser` as a valid value for the `iscsi.transport` StorageClass parameter
- Add a node preflight check that the `isert` kernel module is loaded, with a clear error message if it is not present

## Summary

The driver-side changes are relatively contained — the key change in `buildConnector` is a few lines. The harder parts are TrueNAS exposing an iSER portal via its API, and ensuring the node environment (RDMA hardware, kernel module, open-iscsi build) is correctly configured, which is outside the driver's control.
