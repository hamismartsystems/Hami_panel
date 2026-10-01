package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hamismartsystems/hami_panel/internal/apply"
	"github.com/hamismartsystems/hami_panel/internal/bot"
	"github.com/hamismartsystems/hami_panel/internal/link"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// botCmd runs the sales bot, or manages who gets the reseller rate:
//
//	hami bot serve -db panel.db -token TOKEN -admin ID [-base-url URL]
//	hami bot reseller add|remove|list -db panel.db -id TELEGRAM_ID
func botCmd(args []string) int {
	if len(args) < 1 {
		botUsage()
		return 2
	}
	switch args[0] {
	case "serve":
		return botServe(args[1:])
	case "reseller":
		return botReseller(args[1:])
	}
	botUsage()
	return 2
}

func botUsage() {
	fmt.Fprint(os.Stderr, `usage:
  hami bot serve -db panel.db -token TOKEN -admin TELEGRAM_ID [-base-url URL]
      [-card NUMBER -bank NAME -holder NAME] [-support URL] [-inbound N]
      [-xray-config PATH --reload-cmd CMD]
  hami bot reseller add|remove|list -db panel.db [-id TELEGRAM_ID]
`)
}

func botServe(args []string) int {
	fs := flag.NewFlagSet("bot serve", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	token := fs.String("token", os.Getenv("HAMI_BOT_TOKEN"), "")
	admin := fs.Int64("admin", 0, "telegram id that approves receipts")
	baseURL := fs.String("base-url", "", "where customers fetch subscriptions")
	card := fs.String("card", "", "")
	bank := fs.String("bank", "", "")
	holder := fs.String("holder", "", "")
	brand := fs.String("brand", "", "name customers see on their configs")
	support := fs.String("support", "", "")
	inbound := fs.Int64("inbound", 0, "inbound for new accounts; 0 = least loaded")
	xrayConfig := fs.String("xray-config", "", "")
	reloadCmd := fs.String("reload-cmd", "systemctl restart hami-xray", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "bot serve: -db and -token are required")
		return 2
	}
	if *admin == 0 {
		fmt.Fprintln(os.Stderr,
			"bot serve: -admin is required, otherwise nobody can approve a receipt")
		return 2
	}

	if *brand != "" {
		link.DefaultBrand = *brand
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	applier := &apply.Applier{Store: st, ConfigPath: *xrayConfig}
	if *xrayConfig != "" && *reloadCmd != "" {
		applier.Reload = strings.Fields(*reloadCmd)
	}

	b, err := bot.New(bot.Config{
		Token: *token, AdminID: *admin, Store: st, Apply: applier,
		BaseURL: *baseURL, CardNumber: *card, CardBank: *bank,
		CardHolder: *holder, SupportURL: *support, InboundID: *inbound,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bot: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := b.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "bot: %v\n", err)
		return 1
	}
	return 0
}

func botReseller(args []string) int {
	if len(args) < 1 {
		botUsage()
		return 2
	}
	action := args[0]
	fs := flag.NewFlagSet("bot reseller", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	id := fs.Int64("id", 0, "")
	name := fs.String("username", "", "optional, for the record")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "bot reseller: -db is required")
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	switch action {
	case "list":
		list, err := st.ListResellers()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(list) == 0 {
			fmt.Println("no resellers")
			return 0
		}
		for _, u := range list {
			fmt.Printf("%-12d @%-20s balance %s\n", u.TelegramID, u.Username, bot.Toman(u.Balance))
		}
		return 0
	case "add", "remove":
		if *id == 0 {
			fmt.Fprintln(os.Stderr, "bot reseller: -id is required")
			return 2
		}
		// Creating the row first means a reseller can be set up before
		// they have ever opened the bot.
		if _, err := st.UpsertBotUser(*id, strings.TrimPrefix(*name, "@"), ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := st.SetReseller(*id, action == "add"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		verb := "now pays half"
		if action == "remove" {
			verb = "pays the normal price"
		}
		fmt.Printf("%d %s\n", *id, verb)
		_ = st.AddEvent("info", "bot", fmt.Sprintf("reseller %s: %d", action, *id), "")
		return 0
	}
	botUsage()
	return 2
}
