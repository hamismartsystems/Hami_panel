package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func TestTheOperatorsPrices(t *testing.T) {
	monthly, ok := PlanByKey("monthly")
	if !ok {
		t.Fatal("no monthly plan")
	}
	forever, ok := PlanByKey("forever")
	if !ok {
		t.Fatal("no untimed plan")
	}
	if monthly.PerGB != 20000 || monthly.Days != 30 {
		t.Errorf("monthly is %d toman per GB for %d days", monthly.PerGB, monthly.Days)
	}
	if forever.PerGB != 50000 || forever.Days != 0 {
		t.Errorf("untimed is %d toman per GB, days %d", forever.PerGB, forever.Days)
	}

	for _, tc := range []struct {
		plan  string
		gb    int
		price int64
	}{
		{"monthly", 1, 20000},
		{"monthly", 10, 200000},
		{"monthly", 50, 1000000},
		{"forever", 1, 50000},
		{"forever", 10, 500000},
		{"forever", 30, 1500000},
	} {
		p, _ := PlanByKey(tc.plan)
		q, err := QuoteFor(p, tc.gb, false)
		if err != nil {
			t.Fatal(err)
		}
		if q.Price != tc.price {
			t.Errorf("%s %dGB = %d, want %d", tc.plan, tc.gb, q.Price, tc.price)
		}
	}
}

func TestResellerPaysHalf(t *testing.T) {
	for _, key := range []string{"monthly", "forever"} {
		p, _ := PlanByKey(key)
		for _, gb := range []int{1, 7, 10, 33, 100} {
			normal, _ := QuoteFor(p, gb, false)
			reseller, _ := QuoteFor(p, gb, true)
			if reseller.Price*2 != normal.Price {
				t.Errorf("%s %dGB: reseller pays %d, half of %d is %d",
					key, gb, reseller.Price, normal.Price, normal.Price/2)
			}
			if reseller.ListPrice != normal.Price {
				t.Errorf("%s %dGB: the reseller should still see the %d list price, got %d",
					key, gb, normal.Price, reseller.ListPrice)
			}
			if reseller.Saved() != normal.Price/2 {
				t.Errorf("%s %dGB: saved %d", key, gb, reseller.Saved())
			}
		}
	}
}

func TestNonsenseVolumesAreRefused(t *testing.T) {
	p, _ := PlanByKey("monthly")
	for _, gb := range []int{0, -1, -100, MaxGB + 1, 999999} {
		if _, err := QuoteFor(p, gb, false); err == nil {
			t.Errorf("%d gigabytes was accepted", gb)
		}
	}
}

func TestTomanReadsLikeAPrice(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1,000"}, {20000, "20,000"},
		{200000, "200,000"}, {1500000, "1,500,000"}, {-5000, "-5,000"},
	} {
		if got := Toman(tc.in); got != tc.want {
			t.Errorf("Toman(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

/* ── the wallet, where a mistake is somebody's money ──────────────── */

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestWalletNeverGoesNegative(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertBotUser(111, "someone", "Some"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WalletAdd(111, 100000); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WalletAdd(111, -150000); err == nil {
		t.Fatal("a wallet was allowed to pay more than it held")
	}
	u, _ := st.BotUser(111)
	if u.Balance != 100000 {
		t.Errorf("balance = %d after a refused charge, want it untouched", u.Balance)
	}
	if _, err := st.WalletAdd(111, -100000); err != nil {
		t.Fatalf("spending the exact balance should work: %v", err)
	}
	u, _ = st.BotUser(111)
	if u.Balance != 0 {
		t.Errorf("balance = %d", u.Balance)
	}
}

func TestUpsertKeepsBalanceAndResellerFlag(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertBotUser(222, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WalletAdd(222, 500000); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReseller(222, true); err != nil {
		t.Fatal(err)
	}
	// they say /start again
	if _, err := st.UpsertBotUser(222, "shop-renamed", "Shop"); err != nil {
		t.Fatal(err)
	}
	u, _ := st.BotUser(222)
	if u.Balance != 500000 {
		t.Errorf("saying hello again wiped the balance: %d", u.Balance)
	}
	if !u.IsReseller {
		t.Error("saying hello again removed the reseller rate")
	}
	if u.Username != "shop-renamed" {
		t.Errorf("username not refreshed: %q", u.Username)
	}
}

// Tapping approve twice must not hand out two accounts.
func TestAnOrderIsDeliveredOnce(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertBotUser(333, "buyer", "B"); err != nil {
		t.Fatal(err)
	}
	o := &store.Order{TelegramID: 333, Plan: "monthly", GB: 10,
		Price: 200000, ListPrice: 200000}
	if err := st.CreateOrder(o); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOrder(o.ID, store.OrderPaid, "card"); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachOrderClient(o.ID, 42, "scorp-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOrder(o.ID, store.OrderPaid, "card"); err == nil {
		t.Error("a delivered order was moved back, which would deliver it twice")
	}
	got, _ := st.GetOrder(o.ID)
	if got.Status != store.OrderDelivered || got.ClientID != 42 {
		t.Errorf("order = %+v", got)
	}
}

func TestOrderHistoryIsNewestFirst(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertBotUser(444, "b", "B"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := st.CreateOrder(&store.Order{
			TelegramID: 444, Plan: "monthly", GB: i + 1, Price: 1, ListPrice: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.OrdersOf(444, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].GB != 3 {
		t.Errorf("history = %+v", list)
	}
}

// The first real sale failed because the bot never chose an inbound and
// the provisioner refuses to guess. Nothing here may depend on an
// operator remembering to pin one.
func TestAnInboundIsChosenWhenNoneIsPinned(t *testing.T) {
	st := openStore(t)
	in := &store.Inbound{
		Remark: "Reality-443", Protocol: "vless", Port: 443, Host: "198.51.100.10",
		Transport: "tcp", Security: "none", Enable: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	got, err := st.LeastLoadedInbound()
	if err != nil || got == nil {
		t.Fatalf("with one enabled inbound the panel must pick it: %v", err)
	}
	if got.ID != in.ID {
		t.Errorf("picked inbound %d, want %d", got.ID, in.ID)
	}
}

func TestNoInboundAtAllIsAnError(t *testing.T) {
	st := openStore(t)
	in, err := st.LeastLoadedInbound()
	if err == nil && in != nil {
		t.Error("an empty panel offered an inbound out of nowhere")
	}
}

// Both links have to be under the QR, and the whole thing has to fit in
// a Telegram caption or the delivery silently splits in two.
func TestDeliveryTextCarriesBothLinksAndFitsACaption(t *testing.T) {
	p, _ := PlanByKey("monthly")
	sub := "https://sub.example.com/sub/mlptlds1lej8ep0p"
	cfg := "vless://11111111-2222-3333-4444-555555555555@198.51.100.10:443?" +
		"flow=xtls-rprx-vision&fp=chrome&pbk=AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJKKK" +
		"&security=reality&sid=1122334455667788&sni=www.example.com&spx=%2F&type=tcp" +
		"#Reality-443-tg223351591-1"
	msg := DeliveryText(p, 10, sub, cfg, "https://t.me/support")

	for _, want := range []string{sub, cfg, "10 گیگابایت", "30 روز", "https://t.me/support"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the delivery text is missing %q", want)
		}
	}
	if n := len([]rune(msg)); n > telegramCaptionLimit {
		t.Errorf("a normal delivery is %d characters, over the %d caption limit, "+
			"so it would arrive as two messages", n, telegramCaptionLimit)
	}
}

func TestDeliveryTextForTheUntimedPlan(t *testing.T) {
	p, _ := PlanByKey("forever")
	msg := DeliveryText(p, 50, "https://sub.example.com/sub/x", "vless://y", "")
	if !strings.Contains(msg, "بدون انقضای زمانی") {
		t.Errorf("the untimed plan should not promise an expiry date: %q", msg)
	}
	if strings.Contains(msg, "پشتیبانی") {
		t.Error("a support line appeared with no support url configured")
	}
}
