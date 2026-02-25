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
