package xray

import (
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// A reseller inbound is saved before its first client is sold. While it is
// empty, every other apply must still work: the empty inbound is skipped.
func TestInboundWithoutClientsIsSkippedNotFatal(t *testing.T) {
	st := openMemStore(t)
	empty := store.Inbound{
		Remark: "reseller-empty", Protocol: "vless", Port: 2089, Host: "edge.example",
		Transport: "tcp", Security: "none",
	}
	if err := st.CreateInbound(&empty); err != nil {
		t.Fatal(err)
	}
	live := store.Inbound{
		Remark: "live", Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: "tcp", Security: "none",
	}
	if err := st.CreateInbound(&live); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&store.Client{InboundID: live.ID, UUID: "uuid-l", Email: "l@hami"}); err != nil {
		t.Fatal(err)
	}

	eps, err := endpointsAt(st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].Inbound.ID != live.ID {
		t.Fatalf("expected only the live inbound, got %+v", eps)
	}
	if _, err := Build(eps); err != nil {
		t.Fatalf("build must succeed with an empty inbound present: %v", err)
	}
}
