package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/provision"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

// Bulk sales let a reseller buy many configs in one order. Every config is
// its own account on the reseller's private inbound, and every config is
// sent as its own QR message. A plain-text copy of all of them goes out
// last, and is also kept on the server so nothing is lost if a message is.

const bulkDivider = "----------------------------------------"

// bulkQuickQty are the one-tap quantities; any other number can be typed.
var bulkQuickQty = []int{5, 10, 20, 50}

// askBulk starts a bulk purchase for a reseller.
func (b *Bot) askBulk(ctx context.Context, u *store.BotUser) {
	if !u.IsReseller {
		_ = b.api.send(ctx, u.TelegramID, "خرید گروهی فقط برای فروشنده‌ها فعال است.",
			keyboard([]button{{Text: "بازگشت", Data: "home"}}))
		return
	}
	d := b.get(u.TelegramID)
	d.stage, d.kind, d.qty = "bulk_qty", "bulk", 0

	var rows [][]button
	var row []button
	for _, n := range bulkQuickQty {
		row = append(row, button{Text: fmt.Sprint(n), Data: fmt.Sprintf("bqty:%d", n)})
	}
	rows = append(rows, row)
	rows = append(rows, []button{{Text: "انصراف", Data: "home"}})
	_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
		"چند کانفیگ می‌خواهید؟\nحداکثر %d کانفیگ در هر سفارش.\nیکی از دکمه‌ها را بزنید یا عدد بفرستید.",
		MaxBulk), keyboard(rows...))
}

// bulkQtyChosen records the quantity and asks for the plan.
func (b *Bot) bulkQtyChosen(ctx context.Context, u *store.BotUser, d *draft, n int) {
	if n < 1 || n > MaxBulk {
		_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
			"تعداد باید بین ۱ و %d باشد.", MaxBulk), "")
		return
	}
	d.stage, d.kind, d.qty = "plan", "bulk", n

	var rows [][]button
	for _, p := range Plans {
		label := fmt.Sprintf("%s — هر گیگ %s تومان", p.Name, Toman(p.PerGB))
		rows = append(rows, []button{{Text: label, Data: "bplan:" + p.Key}})
	}
	rows = append(rows, []button{{Text: "بازگشت", Data: "bulk"}})
	_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
		"تعداد: %d کانفیگ\nکدام نوع کانفیگ؟", n), keyboard(rows...))
}

// askBulkGB asks for the volume of each config in the bulk order.
func (b *Bot) askBulkGB(ctx context.Context, u *store.BotUser, planKey string) {
	p, ok := PlanByKey(planKey)
	d := b.get(u.TelegramID)
	if !ok || d.qty < 1 {
		b.askBulk(ctx, u)
		return
	}
	d.stage, d.kind, d.plan = "gb", "bulk", planKey

	var rows [][]button
	var row []button
	for _, g := range []int{10, 20, 30, 50, 100} {
		row = append(row, button{Text: fmt.Sprint(g), Data: fmt.Sprintf("gb:%d", g)})
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []button{{Text: "انصراف", Data: "home"}})

	expiry := "بدون انقضای زمانی"
	if p.Days > 0 {
		expiry = fmt.Sprintf("%d روز از اولین اتصال هر کانفیگ", p.Days)
	}
	_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
		"تعداد: %d کانفیگ · پلن %s\nمدت: %s\n\nحجم هر کانفیگ چند گیگابایت باشد؟ یکی را بزنید یا عدد بفرستید.",
		d.qty, p.Name, expiry), keyboard(rows...))
}

// bulkQuoteAndAsk prices the whole order and offers the payment methods.
func (b *Bot) bulkQuoteAndAsk(ctx context.Context, u *store.BotUser, d *draft, gb int) {
	p, ok := PlanByKey(d.plan)
	if !ok || d.qty < 1 {
		b.sendHome(ctx, u)
		return
	}
	// Refuse before taking money for an order that cannot be delivered.
	if id, _ := b.cfg.Store.ResellerInboundID(u.TelegramID); id == 0 {
		_ = b.api.send(ctx, u.TelegramID,
			"اینباند اختصاصی شما هنوز آماده نشده است. لطفاً با پشتیبانی هماهنگ کنید.", "")
		return
	}
	q, err := QuoteBulk(p, gb, d.qty, u.IsReseller)
	if err != nil {
		_ = b.api.send(ctx, u.TelegramID, err.Error(), "")
		return
	}
	o := &store.Order{
		TelegramID: u.TelegramID, Plan: p.Key, GB: gb, Quantity: d.qty,
		Price: q.Price, ListPrice: q.ListPrice, Status: store.OrderPending,
	}
	if err := b.cfg.Store.CreateOrder(o); err != nil {
		_ = b.api.send(ctx, u.TelegramID, "خطا در ثبت سفارش. دوباره تلاش کنید.", "")
		return
	}
	d.stage, d.gb, d.order = "pay", gb, o.ID

	msg := fmt.Sprintf("سفارش گروهی\n\nتعداد: %d کانفیگ\nپلن: %s\nحجم هر کانفیگ: %d گیگابایت\nمبلغ کل: %s تومان",
		d.qty, p.Name, gb, Toman(q.Price))
	if q.Reseller {
		msg += fmt.Sprintf("\nقیمت مشتری: %s تومان — شما %s تومان کمتر می‌پردازید",
			Toman(q.ListPrice), Toman(q.Saved()))
	}
	msg += fmt.Sprintf("\n\nموجودی کیف پول: %s تومان", Toman(u.Balance))

	rows := [][]button{}
	if u.Balance >= q.Price {
		rows = append(rows, []button{{Text: "پرداخت از کیف پول", Data: fmt.Sprintf("paywallet:%d", o.ID)}})
	}
	rows = append(rows,
		[]button{{Text: "کارت به کارت", Data: fmt.Sprintf("paycard:%d", o.ID)}},
		[]button{{Text: "انصراف", Data: "home"}})
	_ = b.api.send(ctx, u.TelegramID, msg, keyboard(rows...))
}

// bulkItem is one delivered config, ready to be shown or saved.
type bulkItem struct {
	Seq      int
	ClientID int64
	Email    string
	SubURL   string
	Link     string
}

// deliverBulk creates the missing accounts of a bulk order and sends them.
// It is safe to call again after a failure: accounts already recorded for
// the order are reused, never recreated.
func (b *Bot) deliverBulk(ctx context.Context, u *store.BotUser, o *store.Order) {
	if o.Status == store.OrderDelivered {
		return
	}
	p, _ := PlanByKey(o.Plan)
	inboundID, err := b.cfg.Store.ResellerInboundID(u.TelegramID)
	if err != nil || inboundID == 0 {
		b.deliveryFailed(ctx, u, o, fmt.Errorf(
			"this reseller has no private inbound yet (use /resellerinbound %d <port>)", u.TelegramID))
		return
	}

	have := map[int]bool{}
	recorded, _ := b.cfg.Store.OrderClients(o.ID)
	for _, c := range recorded {
		have[c.Seq] = true
	}

	created := 0
	var lastErr error
	for seq := 1; seq <= o.Quantity; seq++ {
		if have[seq] {
			continue
		}
		email := fmt.Sprintf("hami-%d-%02d-%s", o.ID, seq, shortID())
		opts := provision.UserOptions{
			InboundID: inboundID,
			Email:     email,
			QuotaGB:   float64(o.GB),
			IPLimit:   1,
		}
		if p.Days > 0 {
			opts.ActivateDays = p.Days
		}
		c, err := provision.User(b.cfg.Store, opts)
		if err != nil {
			lastErr = fmt.Errorf("config %d of %d: %w", seq, o.Quantity, err)
			break
		}
		if err := b.cfg.Store.AddOrderClient(o.ID, seq, c.ID, c.Email); err != nil {
			// The account exists but is not recorded. Say so loudly, since
			// it cannot be matched to the order automatically.
			lastErr = fmt.Errorf("config %d of %d created as client %d but not recorded: %w",
				seq, o.Quantity, c.ID, err)
			_ = b.cfg.Store.AddEvent("error", "bot", lastErr.Error(), c.Email)
			break
		}
		created++
	}
	if created > 0 && b.cfg.Apply != nil && b.cfg.Apply.Enabled() {
		if err := b.cfg.Apply.Now(); err != nil {
			_ = b.cfg.Store.AddEvent("error", "bot",
				"the core did not pick up the bulk accounts", err.Error())
		}
	}

	items := b.bulkItems(o.ID)
	complete := len(items) == o.Quantity

	// Keep the plain-text copy before anything is sent, so the configs
	// survive even if every message fails.
	text := bulkText(p, o, u, items, b.cfg.SupportURL)
	if path, err := b.saveBulkExport(o.ID, text); err != nil {
		_ = b.cfg.Store.AddEvent("error", "bot", "bulk export not saved", err.Error())
	} else if path != "" {
		_ = b.cfg.Store.AddEvent("info", "bot", "bulk export saved", path)
	}

	if complete {
		// AttachOrderClient marks the order delivered, which is what stops
		// a second approval from producing more accounts.
		_ = b.cfg.Store.AttachOrderClient(o.ID, items[0].ClientID, items[0].Email)
		_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
			"سفارش گروهی شما آماده است: %d کانفیگ.\nهر کانفیگ جدا فرستاده می‌شود و در آخر یک فایل متنی کامل می‌آید.",
			o.Quantity), "")
		for _, it := range items {
			b.sendBulkItem(ctx, u.TelegramID, it, len(items))
		}
		b.sendBulkFile(ctx, u.TelegramID, o.ID, text)
		_ = b.cfg.Store.AddEvent("info", "bot", fmt.Sprintf(
			"bulk order %d delivered: %d configs, %dGB each, %s toman",
			o.ID, len(items), o.GB, Toman(o.Price)), "")
		return
	}

	// Partial: the order stays paid so the operator can approve again and
	// the missing slices are created. The configs already made are still
	// sent now, with the text copy, so the customer is not left empty.
	reason := "unknown error"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	for _, it := range items {
		b.sendBulkItem(ctx, u.TelegramID, it, len(items))
	}
	b.sendBulkFile(ctx, u.TelegramID, o.ID, text)
	b.deliveryFailed(ctx, u, o, fmt.Errorf("bulk order incomplete: %d of %d created: %s",
		len(items), o.Quantity, reason))
}

// bulkItems loads every config an order has produced so far, in order.
func (b *Bot) bulkItems(orderID int64) []bulkItem {
	recorded, err := b.cfg.Store.OrderClients(orderID)
	if err != nil {
		return nil
	}
	var out []bulkItem
	for _, rec := range recorded {
		c, err := b.cfg.Store.GetClient(rec.ClientID)
		if err != nil || c == nil {
			continue
		}
		in, err := b.cfg.Store.GetInbound(c.InboundID)
		if err != nil || in == nil {
			continue
		}
		link, _ := subs.LinkOf(subs.Entry{Inbound: *in, Client: *c})
		subURL := ""
		if b.cfg.BaseURL != "" && c.SubToken != "" {
			subURL = strings.TrimRight(b.cfg.BaseURL, "/") + "/sub/" + c.SubToken
		}
		out = append(out, bulkItem{
			Seq: rec.Seq, ClientID: c.ID, Email: c.Email, SubURL: subURL, Link: link,
		})
	}
	return out
}

// sendBulkItem sends one config as a QR code with its links underneath.
// A failed send is logged and skipped; the text file still has it.
func (b *Bot) sendBulkItem(ctx context.Context, chat int64, it bulkItem, total int) {
	target := it.SubURL
	if target == "" {
		target = it.Link
	}
	if target == "" {
		return
	}
	img, err := qrPNG(target)
	if err != nil {
		_ = b.cfg.Store.AddEvent("error", "bot", "bulk QR not made", err.Error())
		return
	}
	caption := fmt.Sprintf("کانفیگ %d از %d\nنام: %s", it.Seq, total, it.Email)
	if it.SubURL != "" {
		caption += "\n\nلینک ساب:\n" + it.SubURL
	}
	if it.Link != "" {
		caption += "\n\nلینک مستقیم:\n" + it.Link
	}
	if len([]rune(caption)) > telegramCaptionLimit {
		caption = truncate(caption, telegramCaptionLimit)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := b.api.sendPhoto(ctx, chat, "config.png", img, caption, ""); err == nil {
			break
		} else if attempt == 1 {
			_ = b.cfg.Store.AddEvent("error", "bot", "bulk config not sent", err.Error())
		}
		time.Sleep(3 * time.Second)
	}
	// Telegram rate-limits bursts; one message a second is well under it.
	time.Sleep(1200 * time.Millisecond)
}

// sendBulkFile sends the plain-text copy of an order.
func (b *Bot) sendBulkFile(ctx context.Context, chat int64, orderID int64, text string) {
	name := fmt.Sprintf("hami-order-%d.txt", orderID)
	caption := "فایل متنی همه‌ی کانفیگ‌های این سفارش.\nاگر جایی گم شد، همه‌ی لینک‌ها اینجاست."
	if err := b.api.sendDocument(ctx, chat, name, []byte(text), caption); err != nil {
		_ = b.cfg.Store.AddEvent("error", "bot", "bulk text file not sent", err.Error())
	}
}

// bulkText builds the plain-text copy: every config, in order, between
// dashed dividers.
func bulkText(p Plan, o *store.Order, u *store.BotUser, items []bulkItem, support string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "سفارش گروهی شماره %d\n", o.ID)
	fmt.Fprintf(&sb, "تعداد کانفیگ‌ها: %d\n", len(items))
	fmt.Fprintf(&sb, "پلن: %s · حجم هر کانفیگ: %d گیگابایت\n", p.Name, o.GB)
	fmt.Fprintf(&sb, "تاریخ: %s\n", time.Now().Format("2006-01-02 15:04"))
	if support != "" {
		fmt.Fprintf(&sb, "پشتیبانی: %s\n", support)
	}
	sb.WriteString("\n")
	for i, it := range items {
		sb.WriteString(bulkDivider + "\n")
		fmt.Fprintf(&sb, "کانفیگ %d از %d · نام: %s\n", i+1, len(items), it.Email)
		if it.SubURL != "" {
			sb.WriteString("لینک ساب: " + it.SubURL + "\n")
		}
		if it.Link != "" {
			sb.WriteString("لینک مستقیم: " + it.Link + "\n")
		}
	}
	sb.WriteString(bulkDivider + "\n")
	return sb.String()
}

// saveBulkExport writes the text copy under ExportDir, readable by the
// service user only. Returns the path, or "" when the copy is turned off.
func (b *Bot) saveBulkExport(orderID int64, text string) (string, error) {
	if b.cfg.ExportDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(b.cfg.ExportDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(b.cfg.ExportDir, fmt.Sprintf("order-%d.txt", orderID))
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// adminResellerInbound is the operator's command:
//
//	/resellerinbound <telegram id> <port>
//
// It creates the private inbound for a reseller. The port has to be opened
// and relayed by the operator first; the bot does not pick ports itself.
func (b *Bot) adminResellerInbound(ctx context.Context, actor *store.BotUser, text string) {
	if b.cfg.AdminID == 0 || actor.TelegramID != b.cfg.AdminID {
		return
	}
	var tgID int64
	var port int
	if _, err := fmt.Sscanf(strings.TrimSpace(text), "/resellerinbound %d %d", &tgID, &port); err != nil {
		_ = b.api.send(ctx, actor.TelegramID, "استفاده: /resellerinbound <شناسه تلگرام> <پورت>", "")
		return
	}
	if err := b.createResellerInbound(tgID, port); err != nil {
		_ = b.api.send(ctx, actor.TelegramID, "ساخت اینباند انجام نشد: "+err.Error(), "")
		return
	}
	_ = b.api.send(ctx, actor.TelegramID, fmt.Sprintf(
		"اینباند اختصاصی فروشنده %d روی پورت %d ساخته شد.", tgID, port), "")
}

// createResellerInbound builds a private Reality inbound for one reseller
// and points the reseller at it.
func (b *Bot) createResellerInbound(tgID int64, port int) error {
	if b.cfg.ResellerHost == "" || b.cfg.ResellerSNI == "" {
		return fmt.Errorf("the reseller host and SNI are not configured")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range", port)
	}
	u, err := b.cfg.Store.BotUser(tgID)
	if err != nil || u == nil {
		return fmt.Errorf("telegram id %d is not known to the bot", tgID)
	}
	if !u.IsReseller {
		return fmt.Errorf("telegram id %d is not a reseller", tgID)
	}
	if existing, _ := b.cfg.Store.ResellerInboundID(tgID); existing != 0 {
		return fmt.Errorf("this reseller already has inbound %d", existing)
	}
	in, err := provision.Inbound(b.cfg.Store, provision.InboundOptions{
		Template: "vless-reality-tcp",
		Remark:   fmt.Sprintf("reseller-%d", tgID),
		Host:     b.cfg.ResellerHost,
		Port:     port,
		SNI:      b.cfg.ResellerSNI,
		Private:  true,
	})
	if err != nil {
		return err
	}
	if err := b.cfg.Store.SetResellerInbound(tgID, in.ID); err != nil {
		return err
	}
	_ = b.cfg.Store.AddEvent("info", "bot", fmt.Sprintf(
		"reseller %d given private inbound %d on port %d", tgID, in.ID, port), "")
	if b.cfg.Apply != nil && b.cfg.Apply.Enabled() {
		if err := b.cfg.Apply.Now(); err != nil {
			return fmt.Errorf("inbound saved but the core did not pick it up: %w", err)
		}
	}
	return nil
}
