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

func TestLeastLoadedInbound(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()

	// nothing yet → not found
	if _, err := st.LeastLoadedInbound(); err != ErrNotFound {
		t.Fatalf("empty DB must give ErrNotFound, got %v", err)
	}

	n1 := &Node{Name: "n1", Address: "a:1"}
	n2 := &Node{Name: "n2", Address: "a:2"}
	_ = st.CreateNode(n1)
	_ = st.CreateNode(n2)

	mkInbound := func(name string, node int64) *Inbound {
		in := &Inbound{NodeID: node, Remark: name, Protocol: "vless", Port: 400 + int(node),
			Host: name + ".x", Transport: "tcp", Security: "reality", SNI: "s.x", PublicKey: "pk"}
		if err := st.CreateInbound(in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	mkClient := func(inID int64, uuid string) {
		c := &Client{InboundID: inID, UUID: uuid, Enable: true}
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
	}

	in1 := mkInbound("on-n1", n1.ID)
	in2 := mkInbound("on-n2", n2.ID)

	// tie → lowest inbound id wins (deterministic)
	got, err := st.LeastLoadedInbound()
	if err != nil || got.ID != in1.ID {
		t.Fatalf("tie must pick inbounds[0]: %v %v", got, err)
	}

	mkClient(in1.ID, "u-1")
	got, _ = st.LeastLoadedInbound()
	if got.ID != in2.ID {
		t.Fatalf("must pick the emptier inbound (in2), got %d", got.ID)
	}

	mkClient(in2.ID, "u-2")
	mkClient(in2.ID, "u-3")
	got, _ = st.LeastLoadedInbound()
	if got.ID != in1.ID {
		t.Fatalf("must balance back to in1, got %d", got.ID)
	}

	// a down node's inbounds are out of the pool even when emptier
	mkClient(in1.ID, "u-4")
	mkClient(in1.ID, "u-5")
	_ = st.MarkNodeStatus(n2.ID, NodeDown, now)
	got, _ = st.LeastLoadedInbound()
	if got.ID != in1.ID {
		t.Fatalf("down node must be skipped even at higher load, got %d", got.ID)
	}

	// disabled inbound is never picked
	_ = st.MarkNodeStatus(n2.ID, NodeHealthy, now)
	_ = st.SetInboundEnabled(in1.ID, false)
	got, _ = st.LeastLoadedInbound()
	if got.ID != in2.ID {
		t.Fatalf("disabled inbound must be skipped, got %d", got.ID)
	}

	// local (node 0) inbounds are eligible too
	local := &Inbound{Remark: "local", Protocol: "vless", Port: 8443, Host: "l.x",
		Transport: "tcp", Security: "reality", SNI: "s.x", PublicKey: "pk"}
	_ = st.CreateInbound(local) // 0 clients < in2's 2
	got, _ = st.LeastLoadedInbound()
	if got.ID != local.ID {
		t.Fatalf("local inbound (node 0) must be eligible, got %d", got.ID)
	}
}

func TestFirstEnabledInboundOnNode(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	n := &Node{Name: "solo", Address: "a:1"}
	_ = st.CreateNode(n)
	if _, err := st.FirstEnabledInboundOnNode(n.ID); err != ErrNotFound {
		t.Fatalf("node without inbounds → ErrNotFound, got %v", err)
	}
	dis := &Inbound{NodeID: n.ID, Remark: "off", Protocol: "vless", Port: 1,
		Host: "h.x", Transport: "tcp", Security: "reality", SNI: "s.x", PublicKey: "pk"}
	_ = st.CreateInbound(dis)
	_ = st.SetInboundEnabled(dis.ID, false)
	en := &Inbound{NodeID: n.ID, Remark: "on", Protocol: "vless", Port: 2,
		Host: "h.x", Transport: "tcp", Security: "reality", SNI: "s.x", PublicKey: "pk"}
	_ = st.CreateInbound(en)
	got, err := st.FirstEnabledInboundOnNode(n.ID)
	if err != nil || got.ID != en.ID {
		t.Fatalf("must skip disabled and return the enabled inbound: %v %v", got, err)
	}
}
