# Multipath HA Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add multipath iSCSI support for TrueNAS HA by accepting multiple portals, connecting to all of them, and ensuring the dynamic portal ID is used for target provisioning.

**Architecture:** `TRUENAS_ISCSI_PORTAL` is parsed as comma-separated portals by both controller and node; the node's `ISCSIHandler` receives all portals and passes them to `csi-lib-iscsi`'s `Connector.TargetPortals`; the controller resolves the live TrueNAS portal DB ID via `GetPortalID` instead of using the hardcoded default of 1.

**Tech Stack:** Go, `github.com/kubernetes-csi/csi-lib-iscsi/iscsi`, TrueNAS WebSocket API, Kubernetes CSI

---

### Task 1: Change `iscsiPortal string` → `iscsiPortals []string` in driver config

**Files:**
- Modify: `pkg/driver/driver.go`
- Create: `pkg/driver/driver_test.go`

**Step 1: Write the failing test**

Create `pkg/driver/driver_test.go`:

```go
package driver

import (
	"testing"
)

func TestISCSIPortals_Single(t *testing.T) {
	d := &Driver{iscsiPortals: []string{"10.0.0.1:3260"}}
	if got := d.ISCSIPortal(); got != "10.0.0.1:3260" {
		t.Fatalf("ISCSIPortal() = %q, want %q", got, "10.0.0.1:3260")
	}
	if got := d.ISCSIPortals(); len(got) != 1 || got[0] != "10.0.0.1:3260" {
		t.Fatalf("ISCSIPortals() = %v, want [10.0.0.1:3260]", got)
	}
}

func TestISCSIPortals_Multiple(t *testing.T) {
	d := &Driver{iscsiPortals: []string{"10.0.0.1:3260", "10.0.0.2:3260"}}
	if got := d.ISCSIPortal(); got != "10.0.0.1:3260" {
		t.Fatalf("ISCSIPortal() = %q, want primary portal", got)
	}
	if got := d.ISCSIPortals(); len(got) != 2 {
		t.Fatalf("ISCSIPortals() returned %d portals, want 2", len(got))
	}
}

func TestISCSIPortals_Empty(t *testing.T) {
	d := &Driver{iscsiPortals: []string{}}
	if got := d.ISCSIPortal(); got != "" {
		t.Fatalf("ISCSIPortal() on empty slice = %q, want empty string", got)
	}
}
```

**Step 2: Run to verify it fails**

```bash
cd /home/wmo/CSI/truenas-csi
go test ./pkg/driver/ -run TestISCSIPortals -v
```

Expected: compile error — `iscsiPortals` field doesn't exist yet.

**Step 3: Implement changes in `pkg/driver/driver.go`**

In the `Driver` struct, change:
```go
iscsiPortal  string
```
to:
```go
iscsiPortals []string
```

In `DriverConfig`, change:
```go
ISCSIPortal  string
```
to:
```go
ISCSIPortals []string
```

Replace the `ISCSIPortal() string` accessor:
```go
// ISCSIPortal returns the primary (first) iSCSI portal address.
func (d *Driver) ISCSIPortal() string {
	if len(d.iscsiPortals) == 0 {
		return ""
	}
	return d.iscsiPortals[0]
}

// ISCSIPortals returns all configured iSCSI portal addresses.
func (d *Driver) ISCSIPortals() []string {
	return d.iscsiPortals
}
```

In `NewDriver`, replace:
```go
iscsiPortal:  config.ISCSIPortal,
```
with:
```go
iscsiPortals: config.ISCSIPortals,
```

In the `ISCSIPortal` auto-derive block (around line 295), replace the entire block:
```go
if config.ISCSIPortal == "" {
    if parsedURL, err := url.Parse(config.TrueNASURL); err == nil {
        host := parsedURL.Hostname()
        if host != "" {
            config.ISCSIPortal = host + ":3260"
            log.V(LogLevelInfo).Info("Derived iSCSI portal from TrueNAS URL", "iscsiPortal", config.ISCSIPortal)
        }
    }
}
```
with:
```go
if len(config.ISCSIPortals) == 0 {
    if parsedURL, err := url.Parse(config.TrueNASURL); err == nil {
        host := parsedURL.Hostname()
        if host != "" {
            config.ISCSIPortals = []string{host + ":3260"}
            log.V(LogLevelInfo).Info("Derived iSCSI portal from TrueNAS URL", "iscsiPortal", host+":3260")
        }
    }
}
```

Also update `reconstructVolumeFromTrueNAS` — it references `d.iscsiPortal` directly in two places. Change both to `d.ISCSIPortal()`.

**Step 4: Run tests**

```bash
go test ./pkg/driver/ -run TestISCSIPortals -v
```

Expected: PASS (3 tests).

Also verify the package still compiles with no errors:
```bash
go build ./pkg/driver/
```

**Step 5: Commit**

```bash
git add pkg/driver/driver.go pkg/driver/driver_test.go
git commit -m "feat: change iscsiPortal string to iscsiPortals slice in driver config"
```

---

### Task 2: Parse comma-separated portals in `cmd/main.go`

**Files:**
- Modify: `cmd/main.go`

**Step 1: Update `loadEnvConfig`**

In `cmd/main.go`, add `"strings"` to the import block (it may already be there).

Replace:
```go
if val := os.Getenv("TRUENAS_ISCSI_PORTAL"); val != "" {
    config.ISCSIPortal = val
}
```
with:
```go
if val := os.Getenv("TRUENAS_ISCSI_PORTAL"); val != "" {
    for _, p := range strings.Split(val, ",") {
        p = strings.TrimSpace(p)
        if p != "" {
            config.ISCSIPortals = append(config.ISCSIPortals, p)
        }
    }
}
```

**Step 2: Verify it compiles**

```bash
go build ./cmd/
```

Expected: no errors.

**Step 3: Commit**

```bash
git add cmd/main.go
git commit -m "feat: parse TRUENAS_ISCSI_PORTAL as comma-separated list"
```

---

### Task 3: Add portals to `ISCSIHandler` and update `buildConnector`

**Files:**
- Modify: `pkg/driver/iscsi.go`
- Modify: `pkg/driver/node.go` (NewNodeServer call to NewISCSIHandler)
- Modify: `pkg/driver/driver_test.go` (add buildConnector test)

**Step 1: Write the failing test**

Add to `pkg/driver/driver_test.go`:

```go
func TestBuildConnector_SinglePortal(t *testing.T) {
	h := &ISCSIHandler{portals: []string{"10.0.0.1:3260"}}
	cfg := &ISCSIConfig{
		TargetIQN: "iqn.2000-01.io.truenas:test",
		LUN:       0,
	}
	c := h.buildConnector("vol-1", cfg)
	if len(c.TargetPortals) != 1 || c.TargetPortals[0] != "10.0.0.1:3260" {
		t.Fatalf("TargetPortals = %v, want [10.0.0.1:3260]", c.TargetPortals)
	}
	if c.Multipath {
		t.Fatal("Multipath should be false when MultipathEnabled is false")
	}
}

func TestBuildConnector_MultiplePortals(t *testing.T) {
	h := &ISCSIHandler{portals: []string{"10.0.0.1:3260", "10.0.0.2:3260"}}
	cfg := &ISCSIConfig{
		TargetIQN:        "iqn.2000-01.io.truenas:test",
		LUN:              0,
		MultipathEnabled: true,
	}
	c := h.buildConnector("vol-1", cfg)
	if len(c.TargetPortals) != 2 {
		t.Fatalf("TargetPortals = %v, want 2 portals", c.TargetPortals)
	}
	if !c.Multipath {
		t.Fatal("Multipath should be true when MultipathEnabled is true")
	}
}
```

**Step 2: Run to verify it fails**

```bash
go test ./pkg/driver/ -run TestBuildConnector -v
```

Expected: compile error — `ISCSIHandler` has no `portals` field yet.

**Step 3: Update `pkg/driver/iscsi.go`**

Add `portals []string` to `ISCSIHandler`:
```go
type ISCSIHandler struct {
	mounter *mount.SafeFormatAndMount
	resizer *mount.ResizeFs
	log     logr.Logger
	portals []string
}
```

Update `NewISCSIHandler` signature:
```go
func NewISCSIHandler(mounter *mount.SafeFormatAndMount, log logr.Logger, portals []string) (*ISCSIHandler, error) {
```

Store portals in the returned handler:
```go
return &ISCSIHandler{
    mounter: mounter,
    resizer: mount.NewResizeFs(mounter.Exec),
    log:     log,
    portals: portals,
}, nil
```

Update `buildConnector` — replace `TargetPortals: []string{config.TargetPortal}` with `h.portals`, and wire multipath:
```go
connector := &iscsilib.Connector{
    VolumeName:    volumeID,
    TargetIqn:     config.TargetIQN,
    TargetPortals: h.portals,
    Lun:           config.LUN,
    RetryCount:    iscsiRetryCount,
    CheckInterval: iscsiCheckInterval,
    DoDiscovery:   true,
    Multipath:     config.MultipathEnabled,
}
```

Also wire `PersistentSessions` — after the connector is built, add:
```go
if config.PersistentSessions {
    connector.DoCHAPDiscovery = false // let iscsid manage reconnect
}
```

**Step 4: Update `pkg/driver/node.go`**

In `NewNodeServer`, update the call:
```go
iscsiHandler, err := NewISCSIHandler(safeMounter, cfg.Driver.Log(), cfg.Driver.ISCSIPortals())
```

**Step 5: Run tests**

```bash
go test ./pkg/driver/ -run TestBuildConnector -v
```

Expected: PASS (2 tests).

```bash
go build ./...
```

Expected: no errors.

**Step 6: Commit**

```bash
git add pkg/driver/iscsi.go pkg/driver/node.go pkg/driver/driver_test.go
git commit -m "feat: pass multiple portals to ISCSIHandler and enable multipath in connector"
```

---

### Task 4: Add `GetPortalID` and update `CreateISCSITargetWithAuth` in `storage.go`

**Files:**
- Modify: `pkg/client/storage.go`
- Modify: `pkg/client/storage_test.go`
- Modify: `pkg/client/mock_test.go`

**Step 1: Write the failing tests**

Add to `pkg/client/mock_test.go`:

```go
// MockISCSIPortal returns a mock iSCSI portal response.
func MockISCSIPortal(id int, ips []string, port int) ISCSIPortal {
    listen := make([]ISCSIPortalListen, len(ips))
    for i, ip := range ips {
        listen[i] = ISCSIPortalListen{IP: ip, Port: port}
    }
    return ISCSIPortal{
        ID:     id,
        Tag:    id,
        Listen: listen,
    }
}
```

Add to `pkg/client/storage_test.go`:

```go
func TestGetPortalID_Found(t *testing.T) {
    mock := NewMockTrueNASServer()
    defer mock.Close()

    mock.SetResponse(methodISCSIPortalQuery, MockResponse{
        Result: []ISCSIPortal{
            MockISCSIPortal(1, []string{"10.0.0.1"}, 3260),
            MockISCSIPortal(3, []string{"10.0.0.2"}, 3260),
        },
    })

    client := connectTestClient(t, mock)
    id, err := client.GetPortalID(testContext(t), "10.0.0.2:3260")
    assertNoError(t, err)
    assertEqual(t, id, 3)
}

func TestGetPortalID_NoPort(t *testing.T) {
    mock := NewMockTrueNASServer()
    defer mock.Close()

    mock.SetResponse(methodISCSIPortalQuery, MockResponse{
        Result: []ISCSIPortal{
            MockISCSIPortal(2, []string{"192.168.1.50"}, 3260),
        },
    })

    client := connectTestClient(t, mock)
    // Should work when caller omits port
    id, err := client.GetPortalID(testContext(t), "192.168.1.50")
    assertNoError(t, err)
    assertEqual(t, id, 2)
}

func TestGetPortalID_NotFound(t *testing.T) {
    mock := NewMockTrueNASServer()
    defer mock.Close()

    mock.SetResponse(methodISCSIPortalQuery, MockResponse{
        Result: []ISCSIPortal{
            MockISCSIPortal(1, []string{"10.0.0.1"}, 3260),
        },
    })

    client := connectTestClient(t, mock)
    _, err := client.GetPortalID(testContext(t), "10.0.0.99:3260")
    assertError(t, err)
}

func TestCreateISCSITargetWithAuth_DynamicPortal(t *testing.T) {
    mock := NewMockTrueNASServer()
    defer mock.Close()

    mock.SetResponse(methodISCSIPortalQuery, MockResponse{
        Result: []ISCSIPortal{
            MockISCSIPortal(7, []string{"10.0.0.1"}, 3260),
        },
    })
    mock.SetResponse(methodISCSITargetCreate, MockResponse{
        Result: MockISCSITarget(1, "test-target", "alias"),
    })

    client := connectTestClient(t, mock)
    target, err := client.CreateISCSITargetWithAuth(testContext(t), "test-target", "alias", 0, 0, "10.0.0.1:3260")
    assertNoError(t, err)
    assertNotNil(t, target)

    // Verify the portal ID sent to TrueNAS was 7, not 1
    params := getRequestParams[[]any](t, mock, methodISCSITargetCreate)
    data, _ := json.Marshal(params[0])
    if !contains(string(data), `"portal":7`) {
        t.Fatalf("expected portal ID 7 in request, got: %s", data)
    }
}
```

**Step 2: Run to verify they fail**

```bash
go test ./pkg/client/ -run "TestGetPortalID|TestCreateISCSITargetWithAuth_DynamicPortal" -v
```

Expected: compile errors — `methodISCSIPortalQuery` and `GetPortalID` don't exist.

**Step 3: Implement in `pkg/client/storage.go`**

Add the constant alongside the other iSCSI method constants:
```go
methodISCSIPortalQuery = "iscsi.portal.query"
```

Remove `defaultISCSIPortalID = 1` from the defaults block (it will no longer be used).

Add `GetPortalID` after the existing portal type definitions:
```go
// GetPortalID returns the TrueNAS database ID for the portal that listens on
// the given address. portalAddr may include a port (e.g. "10.0.0.1:3260") or
// be just an IP ("10.0.0.1") — the port is stripped before matching.
func (c *Client) GetPortalID(ctx context.Context, portalAddr string) (int, error) {
    // Strip port if present
    host := portalAddr
    if h, _, err := net.SplitHostPort(portalAddr); err == nil {
        host = h
    }

    var portals []ISCSIPortal
    if err := c.Call(ctx, methodISCSIPortalQuery, []any{}, &portals); err != nil {
        return 0, fmt.Errorf("failed to query iSCSI portals: %w", err)
    }

    for _, p := range portals {
        for _, l := range p.Listen {
            if l.IP == host {
                return p.ID, nil
            }
        }
    }

    return 0, fmt.Errorf("no iSCSI portal found for address %q", portalAddr)
}
```

Note: add `"net"` to the import block in `storage.go`.

Update `CreateISCSITargetWithAuth` signature to accept `portalAddr string`:
```go
func (c *Client) CreateISCSITargetWithAuth(ctx context.Context, name, alias string, authTag, initiatorID int, portalAddr string) (*ISCSITarget, error) {
```

Replace `Portal: defaultISCSIPortalID` inside the function with a dynamic lookup:
```go
portalID, err := c.GetPortalID(ctx, portalAddr)
if err != nil {
    return nil, fmt.Errorf("failed to resolve portal ID for %q: %w", portalAddr, err)
}
group := ISCSITargetGroup{
    Portal: portalID,
}
```

Update the `CreateISCSITarget` convenience wrapper:
```go
func (c *Client) CreateISCSITarget(ctx context.Context, name, alias, portalAddr string) (*ISCSITarget, error) {
    return c.CreateISCSITargetWithAuth(ctx, name, alias, 0, 0, portalAddr)
}
```

**Step 4: Run tests**

```bash
go test ./pkg/client/ -run "TestGetPortalID|TestCreateISCSITargetWithAuth_DynamicPortal" -v
```

Expected: PASS (4 tests).

**Step 5: Commit**

```bash
git add pkg/client/storage.go pkg/client/storage_test.go pkg/client/mock_test.go
git commit -m "feat: add GetPortalID and dynamic portal ID resolution in CreateISCSITargetWithAuth"
```

---

### Task 5: Update `controller.go` call sites to pass `portalAddr`

**Files:**
- Modify: `pkg/driver/controller.go`

**Step 1: Fix compile errors**

At this point `go build ./...` will fail because `CreateISCSITarget` and `CreateISCSITargetWithAuth` now require a `portalAddr` argument. Fix all call sites by passing `s.driver.ISCSIPortal()`.

There are four call sites to update:

Line ~503:
```go
target, err := s.driver.Client().CreateISCSITargetWithAuth(ctx, targetSuffix, fmt.Sprintf("CSI volume %s", volumeID), 0, 0, s.driver.ISCSIPortal())
```

Line ~537:
```go
target, tErr := s.driver.Client().CreateISCSITargetWithAuth(ctx, targetSuffix, fmt.Sprintf("CSI volume %s", volumeID), 0, 0, s.driver.ISCSIPortal())
```

Line ~686:
```go
target, err := s.driver.Client().CreateISCSITargetWithAuth(ctx, targetSuffix, fmt.Sprintf("CSI volume %s", volumeID), authTag, initiatorID, s.driver.ISCSIPortal())
```

Line ~1032:
```go
target, err := s.driver.Client().CreateISCSITarget(ctx, targetSuffix, fmt.Sprintf("CSI volume clone %s", volumeID), s.driver.ISCSIPortal())
```

**Step 2: Verify it compiles**

```bash
go build ./...
```

Expected: no errors.

**Step 3: Run all tests**

```bash
go test ./pkg/... -v 2>&1 | tail -20
```

Expected: all existing tests pass.

**Step 4: Commit**

```bash
git add pkg/driver/controller.go
git commit -m "feat: pass portal address to CreateISCSITarget call sites in controller"
```

---

### Task 6: Update deploy YAML

**Files:**
- Modify: `deploy/truenas-csi-driver.yaml`

**Step 1: Update ConfigMap**

In the `truenas-csi-config` ConfigMap, change:
```yaml
iscsiPortal: "YOUR-TRUENAS-IP:3260"
```
to:
```yaml
iscsiPortal: "YOUR-TRUENAS-IP-1:3260,YOUR-TRUENAS-IP-2:3260"
```

**Step 2: Extend node DaemonSet postStart hook**

The existing `postStart` command in the `csi-node` container is a single shell string. Extend it to also patch `multipath.conf`. Replace the existing `command` value with:

```yaml
command:
  - /bin/sh
  - -c
  - |
    mkdir -p /run/lock/iscsi
    mv /usr/sbin/iscsiadm /usr/sbin/iscsiadm.orig 2>/dev/null
    printf '#!/bin/sh\nnsenter --mount=/host/proc/1/ns/mnt -- /usr/sbin/iscsiadm "$@"\n' > /usr/sbin/iscsiadm
    chmod +x /usr/sbin/iscsiadm
    if ! grep -q 'vendor.*TrueNAS' /host/etc/multipath/multipath.conf 2>/dev/null; then
      mkdir -p /host/etc/multipath
      printf '\ndevices {\n  device {\n    vendor            "TrueNAS"\n    product           "iSCSI Disk"\n    path_grouping_policy group_by_prio\n    prio              alua\n    path_checker      tur\n    hardware_handler  "1 alua"\n    failback          immediate\n    fast_io_fail_tmo  15\n  }\n}\n' >> /host/etc/multipath/multipath.conf
    fi
```

Note: `/host` is already mounted as `mountPropagation: Bidirectional` in the existing YAML, so no new volume mount is needed.

**Step 3: Verify YAML is valid**

```bash
kubectl apply --dry-run=client -f deploy/truenas-csi-driver.yaml
```

Expected: `... configured (dry run)` with no errors.

**Step 4: Commit**

```bash
git add deploy/truenas-csi-driver.yaml
git commit -m "feat: update deploy for multi-portal and multipath.conf patching"
```

---

### Task 7: Full test run and final verification

**Step 1: Run all unit tests**

```bash
go test ./pkg/... -v 2>&1 | grep -E "^(=== RUN|--- PASS|--- FAIL|FAIL|ok)"
```

Expected: all `ok`, no `FAIL`.

**Step 2: Run build for all targets**

```bash
go build ./...
```

Expected: no errors.

**Step 3: Verify TRUENAS_ISCSI_PORTAL parsing end-to-end**

```bash
TRUENAS_ISCSI_PORTAL="10.0.0.1:3260,10.0.0.2:3260" go run ./cmd/ --node-id=test --endpoint=unix:///tmp/test.sock 2>&1 | head -5
```

Expected: driver starts, logs show two portals derived (will fail to connect to TrueNAS, that's fine — just confirm startup logs).

**Step 4: Final commit if needed**

```bash
git status
```

If any stray files, add and commit them.
