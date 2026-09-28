package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

// noticeCmd lists due subscriber warnings (expiring soon / nearly out of
// quota), logs them, and optionally pushes them to a Telegram chat:
//
//	hami notice -db panel.db [-days 3] [-ratio 0.8] [-telegram BOTTOKEN:CHATID]
//
// Intended for cron: `0 12 * * * hami notice -db /var/lib/hami/panel.db`
func noticeCmd(args []string) int {
	fs := flag.NewFlagSet("notice", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	days := fs.Int("days", 3, "expiry warning window")
	ratio := fs.Float64("ratio", 0.8, "quota warning ratio, 0..1")
	telegram := fs.String("telegram", "", "BOTTOKEN:CHATID to push to Telegram")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *dbPath == "" {
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	clients, err := st.ListAllClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		return 1
	}
	alerts := subs.DueAlerts(clients, time.Now().UTC(), *days, *ratio)
	if len(alerts) == 0 {
		fmt.Println("no alerts due")
		return 0
	}
	var b strings.Builder
	for _, a := range alerts {
		line := fmt.Sprintf("⚠️ %s [%s]: %s", a.Email, a.Kind, a.Detail)
		fmt.Println(line)
		b.WriteString(line + "\n")
		logEvent(*dbPath, "warn", "notice", fmt.Sprintf("%s [%s]: %s", a.Email, a.Kind, a.Detail), "")
	}
	if *telegram != "" {
		if err := sendTelegram(*telegram, b.String()); err != nil {
			fmt.Fprintf(os.Stderr, "telegram: %v\n", err)
			return 1
		}
		fmt.Println("pushed to telegram")
	}
	return 0
}

// sendTelegram posts the message via the Bot API. `tgt` is "token:chatid".
func sendTelegram(tgt, text string) error {
	parts := strings.SplitN(tgt, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf(`telegram target must be "TOKEN:CHATID"`)
	}
	form := url.Values{"chat_id": {parts[1]}, "text": {text}}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.PostForm(
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", parts[0]), form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram api: %s", resp.Status)
	}
	return nil
}
