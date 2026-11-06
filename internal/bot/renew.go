package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/provision"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// ExpiryWarning is shown before every purchase and every renewal. The
// wording was approved by the owner on 1 Nov 2026.
const ExpiryWarning = "⚠️ توجه مهم\n" +
	"سه روز پس از پایان هر سرویس، در صورت عدم تمدید، سرویس از روی سرور حذف می‌شود " +
	"و امکان استفاده‌ی مجدد از لینک آن وجود نخواهد داشت.\n\n" +
	"با ادامه‌ی خرید یا تمدید، این شرط را می‌پذیرید."

// GraceAfterExpiry is how long a bot-sold service stays on the server
// after it ends, before the sweep removes it.
const GraceAfterExpiry = 72 * time.Hour

// renewalDays is the length of one renewal. It equals one monthly purchase.
const renewalDays = 31

// renewedExpiry works out when a renewed service ends.
//   - A service that is still running keeps its remaining days: the
//     renewal adds 31 days to its current end date.
//   - An expired service starts again from now.
//   - A service whose clock has not started yet (first connection still
//     pending) gets no date, so its clock still starts on first use.
func renewedExpiry(c *store.Client, now time.Time) *time.Time {
	if c.ExpireAt == nil && c.ActivateDays > 0 {
		return nil
	}
	base := now.UTC()
	if c.ExpireAt != nil && c.ExpireAt.After(base) {
		base = c.ExpireAt.UTC()
	}
	t := base.Add(renewalDays * 24 * time.Hour)
	return &t
}

// askRenew quotes a renewal of one delivered service and shows the same
// payment choices as a purchase. The renewal is an ordinary order that
// already points at the existing account, so the same approve flow
// handles it; deliver() sees the account and extends it instead of
// creating a new one.
func (b *Bot) askRenew(ctx context.Context, u *store.BotUser, orderID int64) {
	back := keyboard([]button{{Text: "سفارش‌های من", Data: "orders"}},
		[]button{{Text: "بازگشت", Data: "home"}})
	o, err := b.cfg.Store.GetOrder(orderID)
	if err != nil || o == nil || o.TelegramID != u.TelegramID ||
		o.Status != store.OrderDelivered || o.ClientID == 0 {
		_ = b.api.send(ctx, u.TelegramID, "این سرویس قابل تمدید نیست.", back)
		return
	}
	p, ok := PlanByKey("monthly")
	if !ok {
		_ = b.api.send(ctx, u.TelegramID, "تمدید در این لحظه ممکن نیست.", back)
		return
	}
	q, err := QuoteFor(p, o.GB, u.IsReseller)
	if err != nil {
		_ = b.api.send(ctx, u.TelegramID, err.Error(), back)
		return
	}
	ro := &store.Order{
		TelegramID: u.TelegramID, Plan: p.Key, GB: o.GB,
		Price: q.Price, ListPrice: q.ListPrice, Status: store.OrderPending,
		ClientID: o.ClientID, Email: o.Email,
	}
	if err := b.cfg.Store.CreateOrder(ro); err != nil {
		_ = b.api.send(ctx, u.TelegramID, "خطا در ثبت تمدید. دوباره تلاش کنید.", back)
		return
	}
	d := b.get(u.TelegramID)
	d.stage, d.gb, d.order = "pay", o.GB, ro.ID

	msg := fmt.Sprintf("تمدید سرویس\n\nایمیل: %s\nحجم: %d گیگابایت\nمدت: %d روز\nمبلغ: %s تومان",
		o.Email, o.GB, renewalDays, Toman(q.Price))
	if q.Reseller {
		msg += fmt.Sprintf("\nقیمت مشتری: %s تومان — شما %s تومان کمتر می‌پردازید",
			Toman(q.ListPrice), Toman(q.Saved()))
	}
	msg += "\n\n" + ExpiryWarning
	msg += fmt.Sprintf("\n\nموجودی کیف پول: %s تومان", Toman(u.Balance))

	rows := [][]button{}
	if u.Balance >= q.Price {
		rows = append(rows, []button{{Text: "پرداخت از کیف پول", Data: fmt.Sprintf("paywallet:%d", ro.ID)}})
	}
	rows = append(rows,
		[]button{{Text: "کارت به کارت", Data: fmt.Sprintf("paycard:%d", ro.ID)}},
		[]button{{Text: "انصراف", Data: "home"}})
	_ = b.api.send(ctx, u.TelegramID, msg, keyboard(rows...))
}

// renewDelivered extends an existing account once the operator has
// approved the payment. It never creates a new account.
func (b *Bot) renewDelivered(ctx context.Context, u *store.BotUser, o *store.Order) {
	st := b.cfg.Store
	c, err := st.GetClient(o.ClientID)
	if err != nil || c == nil {
		b.renewalFailed(ctx, u, o, fmt.Errorf("account %d was not found", o.ClientID))
		return
	}
	c.ExpireAt = renewedExpiry(c, time.Now())
	c.TotalBytes = provision.GBToBytes(float64(o.GB))
	c.Enable = true
	if err := st.UpdateClient(c); err != nil {
		b.renewalFailed(ctx, u, o, err)
		return
	}
	// A renewal starts the new period with a clean usage counter.
	if _, _, err := st.ResetClientUsage(c.ID); err != nil {
		b.renewalFailed(ctx, u, o, err)
		return
	}
	if err := st.AttachOrderClient(o.ID, c.ID, c.Email); err != nil {
		b.renewalFailed(ctx, u, o, err)
		return
	}
	if b.cfg.Apply.Enabled() {
		if err := b.cfg.Apply.Now(); err != nil {
			_ = st.AddEvent("error", "bot", "the core did not pick up a renewal", err.Error())
		}
	}
	until := "تا شروع اولین اتصال"
	if c.ExpireAt != nil {
		until = c.ExpireAt.Format("2006-01-02")
	}
	_ = st.AddEvent("info", "bot", fmt.Sprintf("renewed order %d: %dGB until %s", o.ID, o.GB, until), c.Email)
	alog("renewal order=%d client=%d until=%s", o.ID, c.ID, until)

	_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
		"سرویس شما تمدید شد.\n\nایمیل: %s\nحجم: %d گیگابایت\nاعتبار تا: %s",
		c.Email, o.GB, until),
		keyboard([]button{{Text: "سفارش‌های من", Data: "orders"}},
			[]button{{Text: "بازگشت", Data: "home"}}))
	if b.cfg.AdminID != 0 {
		_ = b.api.send(ctx, b.cfg.AdminID, fmt.Sprintf("تمدید انجام شد: %s (%d گیگ)", c.Email, o.GB), "")
	}
}

func (b *Bot) renewalFailed(ctx context.Context, u *store.BotUser, o *store.Order, err error) {
	_ = b.cfg.Store.AddEvent("error", "bot", fmt.Sprintf("renewal of order %d failed", o.ID), err.Error())
	alog("renewal failed order=%d: %v", o.ID, err)
	_ = b.api.send(ctx, u.TelegramID,
		"تمدید انجام نشد. پیام شما ثبت شد و به پشتیبانی اطلاع داده می‌شود.", "")
	if b.cfg.AdminID != 0 {
		_ = b.api.send(ctx, b.cfg.AdminID, fmt.Sprintf("خطا در تمدید سفارش %d: %v", o.ID, err), "")
	}
}
