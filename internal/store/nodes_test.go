package store

import (
	"errors"
	"testing"
	"time"
)

func TestNodeLifecycleAndStatus(t *testing.T) {
	s := openMem(t)
	n := &Node{Name: "fr-1", Address: "10.0.0.5:9100"}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n.APIKey == "" || len(n.APIKey) < 32 {
		t.Fatalf("api key not generated: %q", n.APIKey)
	}
	if n.Status != NodeUnknown {
		t.Fatalf("initial status: %q", n.Status)
	}
	now := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	if err := s.MarkNodeStatus(n.ID, NodeHealthy, now); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != NodeHealthy || got.LastSeen == nil || !got.LastSeen.Equal(now) {
		t.Fatalf("status not persisted: %+v", got)
	}
	byName, err := s.GetNodeByName("fr-1")
	if err != nil || byName.ID != n.ID {
		t.Fatalf("by name: %v", err)
	}
	if _, err := s.GetNodeByName("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteNodeDetachesInbounds(t *testing.T) {
	s := openMem(t)
	n := &Node{Name: "nl-1", Address: "10.0.0.9:9100"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	in := &Inbound{NodeID: n.ID, Remark: "nl", Protocol: "vless", Port: 443, Host: "n.nl",
		Transport: "tcp", Security: "reality", SNI: "cdn.x", PublicKey: "pk"}
	if err := s.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetInbound(in.ID); got.NodeID != n.ID {
		t.Fatalf("inbound not attached: %+v", got)
	}
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	// inbound survives, falls back to this server
	if got, err := s.GetInbound(in.ID); err != nil || got.NodeID != 0 {
		t.Fatalf("inbound must survive detached: %+v %v", got, err)
	}
	if err := s.DeleteNode(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
