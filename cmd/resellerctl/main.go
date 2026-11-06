// resellerctl creates one private inbound per reseller, binds it to the
// reseller, and adds one first client so the inbound is never empty when
// the config is applied. It uses the same calls as the bot's
// /resellerinbound command, in the same order.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/apply"
	"github.com/hamismartsystems/hami_panel/internal/provision"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

func main() {
	dbPath := flag.String("db", "/etc/hami/panel.db", "panel database")
	tmpl := flag.String("template", "vless-reality-tcp", "inbound template")
	host := flag.String("host", "edge.hamismartsystems.shop", "public host")
	sni := flag.String("sni", "www.samsung.com", "reality sni")
	xrayCfg := flag.String("xray-config", "/etc/hami/xray.json", "xray config path")
	reload := flag.String("reload-cmd", "systemctl restart hami-xray", "reload command; empty for none")
	flag.Parse()
	// each argument: <telegram_id>:<port>
	specs := flag.Args()
	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: resellerctl [flags] <telegram_id>:<port> ...")
		os.Exit(2)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		fail("open db: %v", err)
	}
	defer st.Close()

	type plan struct {
		tg   int64
		port int
	}
	var plans []plan
	for _, s := range specs {
		parts := strings.Split(s, ":")
		if len(parts) != 2 {
			fail("bad spec %q", s)
		}
		tg, err1 := strconv.ParseInt(parts[0], 10, 64)
		port, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			fail("bad spec %q", s)
		}
		plans = append(plans, plan{tg, port})
	}

	// Check everything before writing anything.
	for _, p := range plans {
		u, err := st.BotUser(p.tg)
		if err != nil || u == nil {
			fail("telegram id %d not known", p.tg)
		}
		if !u.IsReseller {
			fail("telegram id %d is not a reseller", p.tg)
		}
		if id, _ := st.ResellerInboundID(p.tg); id != 0 {
			fail("telegram id %d already has inbound %d", p.tg, id)
		}
		if _, err := st.GetClientByEmail(fmt.Sprintf("reseller-%d-01", p.tg)); err == nil {
			fail("client reseller-%d-01 already exists", p.tg)
		}
	}

	for _, p := range plans {
		in, err := provision.Inbound(st, provision.InboundOptions{
			Template: *tmpl,
			Remark:   fmt.Sprintf("reseller-%d", p.tg),
			Host:     *host,
			Port:     p.port,
			SNI:      *sni,
			Private:  true,
		})
		if err != nil {
			fail("inbound for %d: %v", p.tg, err)
		}
		if err := st.SetResellerInbound(p.tg, in.ID); err != nil {
			fail("link %d: %v", p.tg, err)
		}
		c, err := provision.User(st, provision.UserOptions{
			InboundID: in.ID,
			Email:     fmt.Sprintf("reseller-%d-01", p.tg),
			IPLimit:   1,
		})
		if err != nil {
			fail("first client for %d: %v", p.tg, err)
		}
		fmt.Printf("ok: telegram %d -> inbound %d port %d, first client %s (client id %d)\n",
			p.tg, in.ID, p.port, c.Email, c.ID)
	}

	a := &apply.Applier{Store: st, ConfigPath: *xrayCfg}
	if *reload != "" {
		a.Reload = strings.Fields(*reload)
	}
	if err := a.Now(); err != nil {
		fail("apply: %v", err)
	}
	fmt.Println("applied: config written and core reloaded")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "resellerctl: "+format+"\n", args...)
	os.Exit(1)
}
