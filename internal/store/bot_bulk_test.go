package store

import (
	"path/filepath"
	"testing"
)

func TestBulkRecordsAndResellerInbound(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.UpsertBotUser(42, "r", "R"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ResellerInboundID(42); got != 0 {
		t.Fatalf("new reseller should have no inbound, got %d", got)
	}
	if err := st.SetResellerInbound(42, 7); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ResellerInboundID(42); got != 7 {
		t.Fatalf("inbound = %d, want 7", got)
	}
	if err := st.SetResellerInbound(999, 7); err == nil {
		t.Fatal("unknown telegram id should be refused")
	}

	o := &Order{TelegramID: 42, Plan: "monthly", GB: 10, Quantity: 3, Price: 300, ListPrice: 600}
	if err := st.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	back, err := st.GetOrder(o.ID)
	if err != nil || back == nil || back.Quantity != 3 {
		t.Fatalf("quantity did not round-trip: %+v %v", back, err)
	}

	if err := st.AddOrderClient(o.ID, 1, 100, "a"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddOrderClient(o.ID, 2, 101, "b"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddOrderClient(o.ID, 2, 102, "dup"); err == nil {
		t.Fatal("a second record for the same slice must be refused")
	}
	list, err := st.OrderClients(o.ID)
	if err != nil || len(list) != 2 || list[0].Seq != 1 || list[1].ClientID != 101 {
		t.Fatalf("OrderClients = %+v, %v", list, err)
	}
}

// The live panel database is at schema version 31 while this source lists
// fewer statements. Opening such a database must still add the bulk-sale
// columns and table, and must not replay anything the live one has.
func TestBulkMigrationsReachALiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewind to what the live database looks like: version 31, none of the
	// bulk-sale pieces present.
	for _, q := range []string{
		`DROP TABLE bot_order_clients`,
		`ALTER TABLE bot_orders DROP COLUMN quantity`,
		`ALTER TABLE bot_users DROP COLUMN reseller_inbound_id`,
		`PRAGMA user_version = 31`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("rewind %q: %v", q, err)
		}
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	if _, err := st.db.Exec(`SELECT quantity FROM bot_orders`); err != nil {
		t.Fatalf("quantity column missing after upgrade: %v", err)
	}
	if _, err := st.db.Exec(`SELECT reseller_inbound_id FROM bot_users`); err != nil {
		t.Fatalf("reseller_inbound_id column missing after upgrade: %v", err)
	}
	if _, err := st.db.Exec(`SELECT order_id FROM bot_order_clients`); err != nil {
		t.Fatalf("bot_order_clients missing after upgrade: %v", err)
	}
}
