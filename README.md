# TrueNAS CSI Driver - Demo Guide

This guide will help you run the TrueNAS CSI driver demo on a local Kubernetes cluster.

## Overview

The `demo-simple.sh` script provides an interactive demonstration of the TrueNAS CSI driver's capabilities:
- NFS volume provisioning
- iSCSI volume provisioning
- Volume expansion
- Volume cloning
- Volume snapshots
- Multiple volume creation

This demo runs entirely on a local Kind (Kubernetes in Docker) cluster and provisions real storage on your TrueNAS system.

## Prerequisites

Before running the demo, ensure you have the following tools installed:

### 1. Docker
- **Purpose**: Required for Kind cluster and building container images
- **Installation**: https://docs.docker.com/get-docker/
- **Verify**: `docker --version`

### 2. Kind (Kubernetes in Docker)
- **Purpose**: Creates a local Kubernetes cluster for testing
- **Installation**: https://kind.sigs.k8s.io/docs/user/quick-start/#installation
- **Verify**: `kind --version`

### 3. kubectl
- **Purpose**: Kubernetes command-line tool
- **Installation**: https://kubernetes.io/docs/tasks/tools/
- **Verify**: `kubectl version --client`

### 4. TrueNAS System
- **Version**: TrueNAS SCALE (tested with v25.10+)
- **Requirements**:
  - Network access from your machine to TrueNAS
  - API access enabled
  - At least one ZFS pool created
  - API key or username/password credentials

## Quick Start

### Option 1: Interactive Setup (Easiest)

Simply run the script and follow the prompts:

```bash
chmod +x ./demo-simple.sh && ./demo-simple.sh
```

The script will:
1. Check for prerequisites
2. Prompt you for TrueNAS connection details
3. Create a Kind cluster (if needed)
4. Build and deploy the CSI driver
5. Launch an interactive demo menu

### Option 2: Using Environment Variables (Automated)

For a non-interactive experience, set environment variables before running:

#### With API Key (Recommended):
```bash
export TRUENAS_IP="10.0.0.136"
export TRUENAS_API_KEY="1-abc123xyz..."
export TRUENAS_POOL="tank"            # Optional, defaults to "tank"
export TRUENAS_USE_WSS="n"            # Optional, "y" for wss://, "n" for ws://

./demo-simple.sh
```

#### With Username/Password:
```bash
export TRUENAS_IP="10.0.0.136"
export TRUENAS_USERNAME="admin"
export TRUENAS_PASSWORD="your-password"
export TRUENAS_POOL="tank"            # Optional

./demo-simple.sh
```

## Getting Your TrueNAS API Key

1. Log into your TrueNAS web interface
2. Click your profile icon (top right)
3. Select **API Keys**
4. Click **Add** to create a new API key
5. Give it a name (e.g., "CSI Driver Demo")
6. Copy the generated key
7. Store it securely

## What the Demo Does

### Automatic Setup (First Run)

If no cluster exists, the script will automatically:
1.  Create a Kind cluster with 2 worker nodes
2.  Build the CSI driver Docker image
3.  Load the image into the Kind cluster
4.  Deploy the CSI driver with your TrueNAS credentials
5.  Create StorageClasses for NFS and iSCSI

## Important Notes

### Network Connectivity
- Your machine must be able to reach TrueNAS over the network
- The Kind cluster runs on Docker network `172.18.0.0/16` by default
- TrueNAS must be accessible from both your host and the Docker network

### Authentication Priority
The CSI driver supports both authentication methods:
- Tries **username/password** first (if provided)
- Falls back to **API key** (if username/password not available)
- At least one method must be configured

## Troubleshooting

### "kind not found"
Install Kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation

### "kubectl not found"
Install kubectl: https://kubernetes.io/docs/tasks/tools/

### "docker not found"
Install Docker: https://docs.docker.com/get-docker/

### "Failed to authenticate"
- Ensure your TrueNAS IP is reachable: `ping YOUR_TRUENAS_IP`
- Check if you can access the web UI at `http://YOUR_TRUENAS_IP`
- Verify the API key hasn't been revoked

### "Pool 'tank' not found"
- Verify the pool exists in TrueNAS → Storage → Pools
- Use the correct pool name when prompted (case-sensitive)
- Set `TRUENAS_POOL` environment variable to your actual pool name

### "PVC stuck in Pending"
- Check driver logs: Choose option **10** from the menu
- Look for connection errors or authentication failures
- Verify TrueNAS is accessible from the Kind cluster

### Viewing Detailed Logs
```bash
# Controller logs (volume creation)
kubectl logs -n truenas-csi -l app=truenas-csi-controller -c csi-controller

# Node logs (volume mounting)
kubectl logs -n truenas-csi -l app=truenas-csi-node -c csi-node
```

## Cleanup

### Clean Demo Resources Only
From the menu, choose **Option 11** to delete all demo PVCs while keeping the driver and cluster.

### Clean Everything
```bash
# Delete the entire Kind cluster
kind delete cluster --name truenas-csi-demo
```

This removes the cluster and all associated resources. Your TrueNAS datasets/shares will be deleted automatically thanks to the `reclaimPolicy: Delete` setting.

## Advanced Usage

### Custom Cluster Name
```bash
export KIND_CLUSTER_NAME="my-custom-cluster"
./demo-simple.sh
```

### Skip Confirmation Prompts
Set all required environment variables to avoid interactive prompts:
```bash
export TRUENAS_IP="10.0.0.136"
export TRUENAS_API_KEY="1-abc123..."
export TRUENAS_POOL="tank"
export TRUENAS_USE_WSS="n"

# Note: You'll still need to confirm the configuration summary
# and interact with the demo menu
./demo-simple.sh
```

### Using Secure WebSocket (TLS)
```bash
export TRUENAS_USE_WSS="y"  # Uses wss:// instead of ws://
./demo-simple.sh
```

## Environment Variable Reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `TRUENAS_IP` | Yes* | - | TrueNAS IP address or hostname |
| `TRUENAS_API_KEY` | Yes* | - | TrueNAS API key (option 1) |
| `TRUENAS_USERNAME` | Yes* | - | TrueNAS username (option 2) |
| `TRUENAS_PASSWORD` | Yes* | - | TrueNAS password (option 2) |
| `TRUENAS_POOL` | No | `tank` | ZFS pool name for volume provisioning |
| `TRUENAS_USE_WSS` | No | `n` | Use wss:// (y) or ws:// (n) |

\* Either API key OR username and password is required

