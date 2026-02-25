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
	if c.DoCHAPDiscovery {
		t.Fatal("DoCHAPDiscovery should be false when PersistentSessions is false")
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
	if c.TargetPortals[0] != "10.0.0.1:3260" || c.TargetPortals[1] != "10.0.0.2:3260" {
		t.Fatalf("TargetPortals = %v, want [10.0.0.1:3260 10.0.0.2:3260]", c.TargetPortals)
	}
}
