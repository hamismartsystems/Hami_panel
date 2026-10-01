package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/apply"
	"github.com/hamismartsystems/hami_panel/internal/provision"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

// Config is everything the bot needs to run.
type Config struct {
	Token   string
	AdminID int64 // who approves receipts
	Store   *store.Store
	Apply   *apply.Applier
	BaseURL string // where customers fetch subscriptions

	CardNumber string
	CardBank   string
	CardHolder string
	SupportURL string

	// InboundID is where new accounts are placed. Zero means the least
	// loaded one, which is what the panel already does for the CLI.
	InboundID int64
}

// Bot is the running sales front end.
type Bot struct {
	cfg Config
	api *api

	mu    sync.Mutex
	draft map[int64]*draft // what each person is in the middle of
}

// draft is a purchase being assembled, or a top-up being entered.
type draft struct {
	stage string // plan | gb | pay | topup | receipt
	plan  string
	gb    int
	order int64
	kind  string // buy | topup
	topup int64
}

// New builds a bot.
func New(cfg Config) (*Bot, error) {
	if cfg.Token == "" {
		return nil, errors.New("bot: no token")
	}
	if cfg.Store == nil {
		return nil, errors.New("bot: no database")
	}
	return &Bot{cfg: cfg, api: newAPI(cfg.Token), draft: map[int64]*draft{}}, nil
}

// Run polls until the context is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.api.me(ctx)
	if err != nil {
		return fmt.Errorf("the token was refused: %w", err)
	}
	fmt.Printf("sales bot running as @%s\n", me.Username)
	_ = b.cfg.Store.AddEvent("info", "bot", "sales bot started as @"+me.Username, "")

	offset, _ := b.cfg.Store.BotOffset()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		ups, err := b.api.getUpdates(ctx, offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A network blip must not become a crash loop that re-reads
			// the same updates and sells twice.
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range ups {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.handle(ctx, u)
		}
		if len(ups) > 0 {
			_ = b.cfg.Store.SetBotOffset(offset)
		}
	}
}

func (b *Bot) handle(ctx context.Context, u Update) {
	defer func() {
		// One malformed update must not take the shop down.
		if r := recover(); r != nil {
			_ = b.cfg.Store.AddEvent("error", "bot",
				fmt.Sprintf("recovered while handling an update: %v", r), "")
		}
	}()
	switch {
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u)
	case u.Message != nil:
		b.onMessage(ctx, u)
	}
}

/* ── state helpers ────────────────────────────────────────────────── */

func (b *Bot) get(id int64) *draft {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.draft[id]
	if d == nil {
		d = &draft{}
		b.draft[id] = d
	}
	return d
}

func (b *Bot) clear(id int64) {
	b.mu.Lock()
	delete(b.draft, id)
	b.mu.Unlock()
}

func (b *Bot) user(from *User) (*store.BotUser, error) {
	if from == nil {
		return nil, errors.New("no sender")
	}
	return b.cfg.Store.UpsertBotUser(from.ID, from.Username, from.FirstName)
}

/* ── messages ─────────────────────────────────────────────────────── */

func (b *Bot) onMessage(ctx context.Context, u Update) {
	m := u.Message
	if m.Chat == nil || m.From == nil {
		return
	}
	chat := m.Chat.ID
	bu, err := b.user(m.From)
	if err != nil {
		return
	}

	// a receipt photo
	if len(m.Photo) > 0 {
		b.onReceipt(ctx, bu, m.Photo[len(m.Photo)-1].FileID)
		return
	}

	text := strings.TrimSpace(m.Text)
	switch {
	case text == "/start" || text == "شروع":
		b.clear(chat)
		b.sendHome(ctx, bu)
		return
	case text == "/id":
		_ = b.api.send(ctx, chat, "شناسهٔ عددی شما:\n"+fmt.Sprint(bu.TelegramID), "")
		return
	}

	d := b.get(chat)
	switch d.stage {
	case "gb":
		n, err := strconv.Atoi(persianDigits(text))
		if err != nil || n <= 0 {
			_ = b.api.send(ctx, chat, "لطفاً فقط عدد بفرستید. مثلاً ۱۰", "")
			return
		}
		b.quoteAndAsk(ctx, bu, d, n)
	case "topup":
		n, err := strconv.ParseInt(persianDigits(text), 10, 64)
		if err != nil || n < 10000 {
			_ = b.api.send(ctx, chat, "مبلغ را به تومان و فقط عدد بفرستید. کمترین مبلغ ۱۰۰۰۰", "")
			return
		}
		d.kind, d.topup = "topup", n
		b.askCard(ctx, bu, n, "افزایش موجودی")
	default:
		b.sendHome(ctx, bu)
	}
}

/* ── screens ──────────────────────────────────────────────────────── */

func (b *Bot) sendHome(ctx context.Context, u *store.BotUser) {
	line := fmt.Sprintf("موجودی کیف پول شما: %s تومان", Toman(u.Balance))
	if u.IsReseller {
		line += "\nنرخ شما: نصف قیمت مشتری"
	}
	rows := [][]button{
		{{Text: "خرید کانفیگ", Data: "buy"}},
		{{Text: "کیف پول", Data: "wallet"}, {Text: "سفارش‌های من", Data: "orders"}},
	}
	if b.cfg.SupportURL != "" {
		rows = append(rows, []button{{Text: "پشتیبانی", URL: b.cfg.SupportURL}})
	}
	_ = b.api.send(ctx, u.TelegramID,
		"سلام "+firstName(u)+"\n\nبه ربات فروش خوش آمدید.\n\n"+line,
		keyboard(rows...))
}

func (b *Bot) askPlan(ctx context.Context, u *store.BotUser) {
	d := b.get(u.TelegramID)
	d.stage, d.kind = "plan", "buy"
	var rows [][]button
	for _, p := range Plans {
		label := fmt.Sprintf("%s — هر گیگ %s تومان", p.Name, Toman(p.PerGB))
		rows = append(rows, []button{{Text: label, Data: "plan:" + p.Key}})
	}
	rows = append(rows, []button{{Text: "بازگشت", Data: "home"}})
	_ = b.api.send(ctx, u.TelegramID, "کدام نوع کانفیگ؟", keyboard(rows...))
}

func (b *Bot) askGB(ctx context.Context, u *store.BotUser, planKey string) {
	p, ok := PlanByKey(planKey)
	if !ok {
		b.sendHome(ctx, u)
		return
	}
	d := b.get(u.TelegramID)
	d.stage, d.plan = "gb", planKey

	var rows [][]button
	quick := []int{10, 20, 30, 50, 100}
	var row []button
	for _, g := range quick {
		row = append(row, button{Text: fmt.Sprint(g), Data: fmt.Sprintf("gb:%d", g)})
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []button{{Text: "بازگشت", Data: "buy"}})

	expiry := "بدون انقضای زمانی"
	if p.Days > 0 {
		expiry = fmt.Sprintf("%d روز از لحظهٔ ساخت", p.Days)
	}
	_ = b.api.send(ctx, u.TelegramID,
		fmt.Sprintf("پلن %s\nهر گیگابایت %s تومان\nمدت: %s\n\n"+
			"چند گیگابایت می‌خواهید؟ یکی از دکمه‌ها را بزنید یا عدد بفرستید.",
			p.Name, Toman(p.PerGB), expiry),
		keyboard(rows...))
}

func (b *Bot) quoteAndAsk(ctx context.Context, u *store.BotUser, d *draft, gb int) {
	p, ok := PlanByKey(d.plan)
	if !ok {
		b.sendHome(ctx, u)
		return
	}
	q, err := QuoteFor(p, gb, u.IsReseller)
	if err != nil {
		_ = b.api.send(ctx, u.TelegramID, err.Error(), "")
		return
	}
	o := &store.Order{
		TelegramID: u.TelegramID, Plan: p.Key, GB: gb,
		Price: q.Price, ListPrice: q.ListPrice, Status: store.OrderPending,
	}
	if err := b.cfg.Store.CreateOrder(o); err != nil {
		_ = b.api.send(ctx, u.TelegramID, "خطا در ثبت سفارش. دوباره تلاش کنید.", "")
		return
	}
	d.stage, d.gb, d.order = "pay", gb, o.ID

	msg := fmt.Sprintf("سفارش شما\n\nپلن: %s\nحجم: %d گیگابایت\nمبلغ: %s تومان",
		p.Name, gb, Toman(q.Price))
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

func (b *Bot) askCard(ctx context.Context, u *store.BotUser, amount int64, what string) {
	d := b.get(u.TelegramID)
	d.stage = "receipt"
	_ = b.api.send(ctx, u.TelegramID, fmt.Sprintf(
		"%s\n\nمبلغ %s تومان را به این کارت واریز کنید:\n\n%s\nبانک %s\nبه نام %s\n\n"+
			"سپس عکس رسید را همین‌جا بفرستید. پس از تأیید، %s",
		what, Toman(amount), b.cfg.CardNumber, b.cfg.CardBank, b.cfg.CardHolder,
		map[bool]string{true: "موجودی شما افزایش می‌یابد.",
			false: "کانفیگ برایتان ساخته می‌شود."}[d.kind == "topup"]),
		keyboard([]button{{Text: "انصراف", Data: "home"}}))
}

/* ── callbacks ────────────────────────────────────────────────────── */

func (b *Bot) onCallback(ctx context.Context, u Update) {
	cb := u.CallbackQuery
	if cb.From == nil {
		return
	}
	bu, err := b.user(cb.From)
	if err != nil {
		return
	}
	data := cb.Data
	_ = b.api.answer(ctx, cb.ID, "")

	switch {
	case data == "home":
		b.clear(bu.TelegramID)
		b.sendHome(ctx, bu)
	case data == "buy":
		b.askPlan(ctx, bu)
	case strings.HasPrefix(data, "plan:"):
		b.askGB(ctx, bu, strings.TrimPrefix(data, "plan:"))
	case strings.HasPrefix(data, "gb:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(data, "gb:"))
		b.quoteAndAsk(ctx, bu, b.get(bu.TelegramID), n)
	case data == "wallet":
		b.showWallet(ctx, bu)
	case data == "topup":
		d := b.get(bu.TelegramID)
		d.stage, d.kind = "topup", "topup"
		_ = b.api.send(ctx, bu.TelegramID,
			"چه مبلغی می‌خواهید شارژ کنید؟ به تومان و فقط عدد.",
			keyboard([]button{{Text: "انصراف", Data: "home"}}))
	case data == "orders":
		b.showOrders(ctx, bu)
	case strings.HasPrefix(data, "paywallet:"):
		b.payFromWallet(ctx, bu, idOf(data))
	case strings.HasPrefix(data, "paycard:"):
		o, _ := b.cfg.Store.GetOrder(idOf(data))
		if o == nil {
			b.sendHome(ctx, bu)
			return
		}
		d := b.get(bu.TelegramID)
		d.kind, d.order = "buy", o.ID
		b.askCard(ctx, bu, o.Price, "پرداخت سفارش")
	case strings.HasPrefix(data, "approve:"):
		b.operatorDecision(ctx, bu, data, true)
	case strings.HasPrefix(data, "reject:"):
		b.operatorDecision(ctx, bu, data, false)
	}
}

func (b *Bot) showWallet(ctx context.Context, u *store.BotUser) {
	msg := fmt.Sprintf("کیف پول\n\nموجودی: %s تومان", Toman(u.Balance))
	if u.IsReseller {
		msg += "\nنرخ شما: نصف قیمت مشتری"
	}
	_ = b.api.send(ctx, u.TelegramID, msg, keyboard(
		[]button{{Text: "افزایش موجودی", Data: "topup"}},
		[]button{{Text: "بازگشت", Data: "home"}}))
}

func (b *Bot) showOrders(ctx context.Context, u *store.BotUser) {
	list, err := b.cfg.Store.OrdersOf(u.TelegramID, 10)
	if err != nil || len(list) == 0 {
		_ = b.api.send(ctx, u.TelegramID, "هنوز سفارشی ندارید.",
			keyboard([]button{{Text: "بازگشت", Data: "home"}}))
		return
	}
	var sb strings.Builder
	sb.WriteString("سفارش‌های شما\n")
	for _, o := range list {
		p, _ := PlanByKey(o.Plan)
		sb.WriteString(fmt.Sprintf("\n%s · %d گیگابایت · %s تومان · %s",
			p.Name, o.GB, Toman(o.Price), statusWord(o.Status)))
		if o.Email != "" {
			sb.WriteString("\n" + o.Email)
		}
	}
	_ = b.api.send(ctx, u.TelegramID, sb.String(),
		keyboard([]button{{Text: "بازگشت", Data: "home"}}))
}

func (b *Bot) payFromWallet(ctx context.Context, u *store.BotUser, orderID int64) {
	o, err := b.cfg.Store.GetOrder(orderID)
	if err != nil || o == nil || o.TelegramID != u.TelegramID {
		return
	}
	if o.Status == store.OrderDelivered {
		_ = b.api.send(ctx, u.TelegramID, "این سفارش قبلاً تحویل شده است.", "")
		return
	}
	if _, err := b.cfg.Store.WalletAdd(u.TelegramID, -o.Price); err != nil {
		_ = b.api.send(ctx, u.TelegramID,
			"موجودی کافی نیست. ابتدا کیف پول را شارژ کنید.",
			keyboard([]button{{Text: "افزایش موجودی", Data: "topup"}}))
		return
	}
	_ = b.cfg.Store.MarkOrder(o.ID, store.OrderPaid, "wallet")
	b.clear(u.TelegramID)
	b.deliver(ctx, u, o)
}

/* ── receipts and the operator's decision ─────────────────────────── */

func (b *Bot) onReceipt(ctx context.Context, u *store.BotUser, fileID string) {
	d := b.get(u.TelegramID)
	if d.stage != "receipt" {
		_ = b.api.send(ctx, u.TelegramID,
			"اگر قصد خرید دارید، اول از منو سفارش بدهید.", "")
		return
	}
	who := "@" + u.Username
	if u.Username == "" {
		who = firstName(u)
	}

	var caption, approve, reject string
	if d.kind == "topup" {
		caption = fmt.Sprintf("درخواست شارژ کیف پول\n\nاز: %s (%d)\nمبلغ: %s تومان",
			who, u.TelegramID, Toman(d.topup))
		approve = fmt.Sprintf("approve:topup:%d:%d", u.TelegramID, d.topup)
		reject = fmt.Sprintf("reject:topup:%d:0", u.TelegramID)
	} else {
		o, _ := b.cfg.Store.GetOrder(d.order)
		if o == nil {
			b.sendHome(ctx, u)
			return
		}
		p, _ := PlanByKey(o.Plan)
		caption = fmt.Sprintf("رسید پرداخت سفارش\n\nاز: %s (%d)\nپلن: %s\nحجم: %d گیگابایت\nمبلغ: %s تومان",
			who, u.TelegramID, p.Name, o.GB, Toman(o.Price))
		if o.Price != o.ListPrice {
			caption += fmt.Sprintf("\n(نرخ فروشنده — قیمت مشتری %s تومان)", Toman(o.ListPrice))
		}
		approve = fmt.Sprintf("approve:order:%d:0", o.ID)
		reject = fmt.Sprintf("reject:order:%d:0", o.ID)
		_ = b.cfg.Store.MarkOrder(o.ID, store.OrderAwaiting, "card")
	}

	if b.cfg.AdminID != 0 {
		_ = b.api.forwardPhoto(ctx, b.cfg.AdminID, fileID, caption, keyboard(
			[]button{{Text: "تأیید", Data: approve}},
			[]button{{Text: "رد", Data: reject}}))
	}
	d.stage = ""
	_ = b.api.send(ctx, u.TelegramID,
		"رسید شما دریافت شد و برای بررسی فرستاده شد. نتیجه را همین‌جا می‌گویم.", "")
}

func (b *Bot) operatorDecision(ctx context.Context, actor *store.BotUser, data string, ok bool) {
	if b.cfg.AdminID == 0 || actor.TelegramID != b.cfg.AdminID {
		return // only the operator decides
	}
	parts := strings.Split(data, ":")
	if len(parts) < 4 {
		return
	}
	kind, idStr, amtStr := parts[1], parts[2], parts[3]
	id, _ := strconv.ParseInt(idStr, 10, 64)
	amount, _ := strconv.ParseInt(amtStr, 10, 64)

	if kind == "topup" {
		if !ok {
			_ = b.api.send(ctx, id, "شارژ شما تأیید نشد. اگر پرداخت کرده‌اید رسید واضح‌تری بفرستید.", "")
			_ = b.api.send(ctx, b.cfg.AdminID, "رد شد.", "")
			return
		}
		bal, err := b.cfg.Store.WalletAdd(id, amount)
		if err != nil {
			_ = b.api.send(ctx, b.cfg.AdminID, "خطا در شارژ: "+err.Error(), "")
			return
		}
		_ = b.cfg.Store.AddEvent("info", "bot",
			fmt.Sprintf("wallet topped up by %s for %d", Toman(amount), id), "")
		_ = b.api.send(ctx, id, fmt.Sprintf(
			"شارژ شما تأیید شد.\nموجودی جدید: %s تومان", Toman(bal)),
			keyboard([]button{{Text: "خرید کانفیگ", Data: "buy"}}))
		_ = b.api.send(ctx, b.cfg.AdminID, "تأیید شد.", "")
		return
	}

	o, err := b.cfg.Store.GetOrder(id)
	if err != nil || o == nil {
		return
	}
	if !ok {
		_ = b.cfg.Store.MarkOrder(o.ID, store.OrderRejected, "")
		_ = b.api.send(ctx, o.TelegramID, "رسید شما تأیید نشد. اگر پرداخت کرده‌اید رسید واضح‌تری بفرستید.", "")
		_ = b.api.send(ctx, b.cfg.AdminID, "رد شد.", "")
		return
	}
	if o.Status == store.OrderDelivered {
		_ = b.api.send(ctx, b.cfg.AdminID, "این سفارش قبلاً تحویل شده بود.", "")
		return
	}
	_ = b.cfg.Store.MarkOrder(o.ID, store.OrderPaid, "card")
	buyer, _ := b.cfg.Store.BotUser(o.TelegramID)
	if buyer == nil {
		return
	}
	_ = b.api.send(ctx, b.cfg.AdminID, "تأیید شد — در حال ساخت اکانت.", "")
	b.deliver(ctx, buyer, o)
}

/* ── delivery ─────────────────────────────────────────────────────── */

func (b *Bot) deliver(ctx context.Context, u *store.BotUser, o *store.Order) {
	p, _ := PlanByKey(o.Plan)
	email := fmt.Sprintf("tg%d-%d", o.TelegramID, o.ID)

	// Which inbound the account goes on. The operator can pin one; with
	// nothing pinned the panel picks the least loaded public inbound,
	// the same choice the command line makes for -node auto.
	inboundID := b.cfg.InboundID
	if inboundID == 0 {
		in, err := b.cfg.Store.LeastLoadedInbound()
		if err != nil || in == nil {
			b.deliveryFailed(ctx, u, o, fmt.Errorf(
				"no inbound is available to put the account on: %v", err))
			return
		}
		inboundID = in.ID
	}

	c, err := provision.User(b.cfg.Store, provision.UserOptions{
		InboundID: inboundID,
		Email:     email,
		QuotaGB:   float64(o.GB),
		Days:      p.Days,
		IPLimit:   1,
	})
	if err != nil {
		b.deliveryFailed(ctx, u, o, err)
		return
	}
	_ = b.cfg.Store.AttachOrderClient(o.ID, c.ID, c.Email)

	// The core has to learn about the new account before the customer
	// tries it, or they will tell you the config does not work.
	if b.cfg.Apply.Enabled() {
		if err := b.cfg.Apply.Now(); err != nil {
			_ = b.cfg.Store.AddEvent("error", "bot",
				"the core did not pick up a new account", err.Error())
		}
	}

	link := ""
	if in, err := b.cfg.Store.GetInbound(c.InboundID); err == nil && in != nil {
		if l, err := subs.LinkOf(subs.Entry{Inbound: *in, Client: *c}); err == nil {
			link = l
		}
	}
	subURL := ""
	if b.cfg.BaseURL != "" && c.SubToken != "" {
		subURL = strings.TrimRight(b.cfg.BaseURL, "/") + "/sub/" + c.SubToken
	}

	msg := DeliveryText(p, o.GB, subURL, link, b.cfg.SupportURL)
	menu := keyboard(
		[]button{{Text: "سفارش‌های من", Data: "orders"}},
		[]button{{Text: "بازگشت", Data: "home"}})

	// Everything arrives as one message: the QR with the links written
	// underneath it, so the customer scans or copies from the same place
	// instead of hunting back through the chat. The QR is rendered here,
	// so no third party ever sees the key.
	target := subURL
	if target == "" {
		target = link
	}
	delivered := false
	if target != "" && len([]rune(msg)) <= telegramCaptionLimit {
		if img, err := qrPNG(target); err == nil {
			if err := b.api.sendPhoto(ctx, u.TelegramID, "config.png", img, msg, menu); err == nil {
				delivered = true
			}
		}
	}
	if !delivered {
		// Either the image could not be made or the caption would be
		// longer than Telegram accepts under a photo. The customer still
		// gets everything, just in two messages.
		_ = b.api.send(ctx, u.TelegramID, msg, menu)
		if target != "" {
			if img, err := qrPNG(target); err == nil {
				_ = b.api.sendPhoto(ctx, u.TelegramID, "config.png", img,
					"این کد را در برنامه اسکن کنید", "")
			}
		}
	}
	_ = b.cfg.Store.AddEvent("info", "bot", fmt.Sprintf(
		"delivered order %d: %s, %dGB, %s toman", o.ID, p.Name, o.GB, Toman(o.Price)), email)
}

// deliveryFailed tells both sides the truth and leaves the order in a
// state the operator can retry: the money is recorded as paid, the
// account is not marked delivered, so approving again tries once more.
func (b *Bot) deliveryFailed(ctx context.Context, u *store.BotUser, o *store.Order, err error) {
	_ = b.cfg.Store.AddEvent("error", "bot",
		"could not create the account for order "+fmt.Sprint(o.ID), err.Error())
	_ = b.api.send(ctx, u.TelegramID,
		"پرداخت شما ثبت شد ولی ساخت کانفیگ به مشکل خورد. "+
			"پشتیبانی پیگیری می‌کند و کانفیگ را برایتان می‌فرستد.", "")
	if b.cfg.AdminID != 0 {
		_ = b.api.send(ctx, b.cfg.AdminID, fmt.Sprintf(
			"ساخت اکانت سفارش %d ناموفق بود:\n%v\n\n"+
				"پس از رفع مشکل، دوباره روی تأیید همان رسید بزنید.", o.ID, err),
			keyboard([]button{{Text: "تلاش دوباره", Data: fmt.Sprintf("approve:order:%d:0", o.ID)}}))
	}
}

// DeliveryText is what the customer reads under the QR code. Both links
// belong here: the subscription is the one to add to an app, the direct
// config is for a quick test or an app that cannot take a subscription.
func DeliveryText(p Plan, gb int, subURL, configLink, support string) string {
	expiry := "بدون انقضای زمانی"
	if p.Days > 0 {
		expiry = fmt.Sprintf("%d روز از همین حالا", p.Days)
	}
	msg := fmt.Sprintf("سفارش شما آماده شد\n\nپلن: %s\nحجم: %d گیگابایت\nانقضا: %s\n",
		p.Name, gb, expiry)
	if subURL != "" {
		msg += "\nلینک اشتراک (این را در برنامه اضافه کنید):\n" + subURL + "\n"
	}
	if configLink != "" {
		msg += "\nلینک مستقیم کانفیگ:\n" + configLink + "\n"
	}
	if support != "" {
		msg += "\nپشتیبانی: " + support
	}
	return msg
}

// telegramCaptionLimit is what Telegram accepts under a photo. A longer
// caption is rejected outright, so delivery falls back to two messages
// rather than failing after the customer has paid.
const telegramCaptionLimit = 1024

/* ── small helpers ────────────────────────────────────────────────── */

func idOf(data string) int64 {
	i := strings.LastIndex(data, ":")
	if i < 0 {
		return 0
	}
	n, _ := strconv.ParseInt(data[i+1:], 10, 64)
	return n
}

func firstName(u *store.BotUser) string {
	if u.FirstName != "" {
		return u.FirstName
	}
	if u.Username != "" {
		return u.Username
	}
	return "دوست عزیز"
}

func statusWord(s string) string {
	switch s {
	case store.OrderDelivered:
		return "تحویل شد"
	case store.OrderPaid:
		return "پرداخت شد"
	case store.OrderAwaiting:
		return "در انتظار تأیید"
	case store.OrderRejected:
		return "رد شد"
	}
	return "ثبت شد"
}

// persianDigits lets someone type ۱۰ instead of 10.
func persianDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r == ',' || r == '،' || r == ' ' || r == '٬':
			// ignore grouping
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
