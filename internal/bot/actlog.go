package bot

import (
	"fmt"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/actlog"
)

// alog records one bot action. It never receives message bodies, tokens or links.
func alog(format string, a ...any) {
	actlog.Log("hami-bot", fmt.Sprintf(format, a...))
}

// describeUpdate says which kind of update arrived, with the sender ID and the
// command word only (never the rest of the text).
func describeUpdate(u Update) string {
	switch {
	case u.CallbackQuery != nil:
		if u.CallbackQuery.From != nil {
			return fmt.Sprintf("callback user=%d", u.CallbackQuery.From.ID)
		}
		return "callback"
	case u.Message != nil:
		s := "message"
		if u.Message.From != nil {
			s += fmt.Sprintf(" user=%d", u.Message.From.ID)
		}
		if f := strings.Fields(u.Message.Text); len(f) > 0 && strings.HasPrefix(f[0], "/") {
			s += " command=" + f[0]
		}
		return s
	}
	return "other"
}
