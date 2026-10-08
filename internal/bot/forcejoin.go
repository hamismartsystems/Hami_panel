package bot

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Force-join gate: before a customer can use the shop they must be a
// member of ForceJoinChannel. The check needs this bot to be an
// administrator of that channel; until it is, every lookup fails and the
// gate deliberately fails open so the shop keeps working.

const (
	joinCallback  = "fj:check"
	joinCacheTTL  = 10 * time.Minute
	joinWarnEvery = time.Hour
)

// gate returns true when the update was consumed — either a non-member
// got the join prompt, or the "I joined" callback was answered.
func (b *Bot) gate(ctx context.Context, u Update) bool {
	if strings.TrimSpace(b.cfg.ForceJoinChannel) == "" {
		return false
	}
	var from *User
	var chat int64
	switch {
	case u.Message != nil:
		from = u.Message.From
		if u.Message.Chat != nil {
			chat = u.Message.Chat.ID
		}
	case u.CallbackQuery != nil:
		from = u.CallbackQuery.From
		if u.CallbackQuery.Message != nil && u.CallbackQuery.Message.Chat != nil {
			chat = u.CallbackQuery.Message.Chat.ID
		}
	}
	if from == nil || chat == 0 {
		return false
	}

	// the "I joined" button always gets a fresh, uncached check
	if u.CallbackQuery != nil && u.CallbackQuery.Data == joinCallback {
		b.onJoinCheck(ctx, u.CallbackQuery.ID, from, chat)
		return true
	}

	if from.ID == b.cfg.AdminID {
		return false
	}
	if b.isMember(ctx, from.ID) {
		return false
	}
	b.askJoin(ctx, chat)
	return true
}

// isMember answers with a short-lived cache; failures are treated as
// "member" so a Telegram hiccup never locks paying customers out.
func (b *Bot) isMember(ctx context.Context, uid int64) bool {
	b.mu.Lock()
	if t, ok := b.fj[uid]; ok && time.Since(t) < joinCacheTTL {
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()

	ok, err := b.memberNow(ctx, uid)
	if err != nil {
		b.joinWarn(err)
		return true // fail open
	}
	if ok {
		b.mu.Lock()
		if len(b.fj) > 4096 { // keep the cache small
			b.fj = map[int64]time.Time{}
		}
		b.fj[uid] = time.Now()
		b.mu.Unlock()
	}
	return ok
}

func (b *Bot) memberNow(ctx context.Context, uid int64) (bool, error) {
	v := url.Values{}
	v.Set("chat_id", b.cfg.ForceJoinChannel)
	v.Set("user_id", fmt.Sprint(uid))
	var m struct {
		Status   string `json:"status"`
		IsMember bool   `json:"is_member"`
	}
	if err := b.api.call(ctx, "getChatMember", v, &m); err != nil {
		return false, err
	}
	switch m.Status {
	case "creator", "administrator", "member":
		return true, nil
	case "restricted":
		return m.IsMember, nil
	default: // left | kicked | anything new
		return false, nil
	}
}

func (b *Bot) askJoin(ctx context.Context, chat int64) {
	text := "🔒 برای استفاده از ربات، اول باید عضو کانال علم و فناوری ما بشی:\n\n" +
		"📡 " + b.cfg.ForceJoinChannel + "\n\n" +
		"بعد از عضویت دکمهٔ «عضو شدم ✅» را بزن."
	_ = b.api.send(ctx, chat, text, keyboard(
		[]button{{Text: "📡 عضویت در کانال", URL: "https://t.me/" + strings.TrimPrefix(b.cfg.ForceJoinChannel, "@")}},
		[]button{{Text: "عضو شدم ✅", Data: joinCallback}},
	))
}

func (b *Bot) onJoinCheck(ctx context.Context, cbID string, from *User, chat int64) {
	ok, err := b.memberNow(ctx, from.ID)
	if err != nil {
		b.joinWarn(err)
		ok = true // fail open, consistent with the cached path
	}
	if !ok {
		_ = b.api.answer(ctx, cbID, "هنوز عضو کانال نیستی — اول عضو شو بعد دوباره بزن 🙏")
		b.askJoin(ctx, chat)
		return
	}
	b.mu.Lock()
	b.fj[from.ID] = time.Now()
	b.mu.Unlock()
	_ = b.api.answer(ctx, cbID, "آفرین! عضویت تأیید شد ✅")
	bu, err := b.user(from)
	if err != nil {
		return
	}
	b.clear(chat)
	b.sendHome(ctx, bu)
}

// joinWarn records gate failures at most once an hour.
func (b *Bot) joinWarn(err error) {
	b.mu.Lock()
	quiet := time.Since(b.fjWarn) < joinWarnEvery
	if !quiet {
		b.fjWarn = time.Now()
	}
	b.mu.Unlock()
	if !quiet && b.cfg.Store != nil {
		_ = b.cfg.Store.AddEvent("warn", "forcejoin",
			"membership lookup failed (gate open): "+truncate(err.Error(), 180), "")
	}
}
