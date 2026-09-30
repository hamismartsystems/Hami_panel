package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/provision"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

// userCmd manages subscribers (quota, expiry, tokens, resets).
//
//	hami user create  -db panel.db -inbound 3 -email alice [-uuid U] [-quota GB]
//	                  [-expire-days 30] [-expire-at RFC3339] [-ip-limit N] [-speed kbps]
//	hami user list    -db panel.db
//	hami user show    -db panel.db -email alice
//	hami user set     -db panel.db -email alice [-quota GB] [-expire-days N]
//	                  [-expire-at RFC3339] [-ip-limit N] [-speed kbps]
//	hami user enable|disable -db panel.db -email alice
//	hami user reset   -db panel.db -email alice
//	hami user rotate-token -db panel.db -email alice
//	hami user delete  -db panel.db -email alice
//	hami user inbounds -db panel.db
func userCmd(args []string) int {
	if len(args) < 1 {
		userUsage()
		return 2
	}
	switch args[0] {
	case "create":
		return userCreate(args[1:])
	case "list":
		return userList(args[1:])
	case "show":
		return userShow(args[1:])
	case "set":
		return userSet(args[1:])
	case "enable", "disable":
		return userToggle(args[0], args[1:])
	case "reset":
		return userReset(args[1:])
	case "rotate-token":
		return userRotate(args[1:])
	case "delete":
		return userDelete(args[1:])
	case "inbounds":
		return userInbounds(args[1:])
	}
	userUsage()
	return 2
}

func userUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami user create -db panel.db -inbound N -email NAME [-uuid U] [-quota GB] [-expire-days D] [-expire-at RFC3339] [-ip-limit N] [-speed KBPS]
  hami user list -db panel.db
  hami user show -db panel.db -email NAME
  hami user set -db panel.db -email NAME [-quota GB] [-expire-days D] [-expire-at RFC3339] [-ip-limit N] [-speed KBPS]
  hami user enable|disable -db panel.db -email NAME
  hami user reset -db panel.db -email NAME
  hami user rotate-token -db panel.db -email NAME
  hami user delete -db panel.db -email NAME
  hami user inbounds -db panel.db
`)
}

type dbFlag struct {
	val string
}

func parseFlagSet(name string, args []string, def func(fs *flag.FlagSet)) (*flag.FlagSet, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	def(fs)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return fs, false
	}
	return fs, true
}

func openDBOr(path string) (*store.Store, bool) {
	if path == "" {
		fmt.Fprintln(os.Stderr, "-db is required")
		return nil, false
	}
	st, err := store.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return nil, false
	}
	return st, true
}

func findClientOr(st *store.Store, email string) (*store.Client, bool) {
	if email == "" {
		fmt.Fprintln(os.Stderr, "-email is required")
		return nil, false
	}
	c, err := st.GetClientByEmail(email)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "client %q not found\n", email)
		return nil, false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lookup: %v\n", err)
		return nil, false
	}
	return c, true
}

func gbToBytes(gb float64) int64 { return int64(gb * (1 << 30)) }

func userCreate(args []string) int {
	var dbPath, email, uid, expireAt, nodeRef string
	var inbound int64
	var quotaGB float64
	var expireDays, ipLimit, speedKbps int
	fs, ok := parseFlagSet("user create", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.Int64Var(&inbound, "inbound", 0, "")
		fs.StringVar(&nodeRef, "node", "", "auto | node name | node id — overrides -inbound")
		fs.StringVar(&email, "email", "", "")
		fs.StringVar(&uid, "uuid", "", "")
		fs.Float64Var(&quotaGB, "quota", 0, "GB, 0 = unlimited")
		fs.IntVar(&expireDays, "expire-days", 0, "")
		fs.StringVar(&expireAt, "expire-at", "", "")
		fs.IntVar(&ipLimit, "ip-limit", 0, "")
		fs.IntVar(&speedKbps, "speed", 0, "kbps")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	if nodeRef != "" {
		picked, err := pickInboundForNodeRef(st, nodeRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "-node: %v\n", err)
			return 1
		}
		inbound = picked.ID
		fmt.Printf("🎯 auto-assigned node → inbound %d (%s)\n", picked.ID, picked.Remark)
	}
	if inbound == 0 || email == "" {
		userUsage()
		return 2
	}
	var parsedExpireAt *time.Time
	if expireAt != "" {
		t, err := time.Parse(time.RFC3339, expireAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "-expire-at must be RFC3339: %v\n", err)
			return 2
		}
		parsedExpireAt = &t
	}
	c, err := provision.User(st, provision.UserOptions{
		InboundID: inbound, Email: email, UUID: uid, QuotaGB: quotaGB,
		Days: expireDays, ExpireAt: parsedExpireAt, IPLimit: ipLimit,
		SpeedKbps: speedKbps,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	logEvent(dbPath, "info", "user", fmt.Sprintf("created %s on inbound %d", email, inbound), "")
	fmt.Printf("✅ created client %d\n   uuid: %s\n   sub token: %s\n", c.ID, c.UUID, c.SubToken)
	return 0
}

// pickInboundForNodeRef turns `-node auto|NAME|ID` into the inbound that
// should host the new client. "auto" = the least-loaded enabled inbound whose
// node is not down; a name/id = that node's first enabled inbound.
func pickInboundForNodeRef(st *store.Store, ref string) (*store.Inbound, error) {
	if ref == "auto" {
		in, err := st.LeastLoadedInbound()
		if err != nil {
			return nil, fmt.Errorf("no eligible inbound (none enabled, or every node down)")
		}
		return in, nil
	}
	nodeID, err := resolveNodeID(st, ref)
	if err != nil {
		return nil, err
	}
	in, err := st.FirstEnabledInboundOnNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("node %s has no enabled inbound", ref)
	}
	return in, nil
}

func userList(args []string) int {
	var dbPath string
	fs, ok := parseFlagSet("user list", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	clients, err := st.ListAllClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		return 1
	}
	fmt.Printf("%-4s %-6s %-18s %-12s %-10s %-8s %s\n", "ID", "INB", "EMAIL", "USAGE/TOTAL", "EXPIRE", "ACTIVE", "SUB")
	for _, c := range clients {
		exp := "—"
		if c.ExpireAt != nil {
			exp = c.ExpireAt.UTC().Format("2006-01-02")
		}
		active := "yes"
		if !subs.ActiveNow(c, time.Now()) {
			active = "NO (" + subs.WhyInactive(c, time.Now()) + ")"
		}
		sub := "—"
		if c.SubToken != "" {
			sub = "✔"
		}
		fmt.Printf("%-4d %-6d %-18s %-12s %-10s %-8s %s\n", c.ID, c.InboundID, c.Email,
			fmt.Sprintf("%s/%s", prettyBytes(c.UpBytes+c.DownBytes), quotaOrInf(c.TotalBytes)), exp, active, sub)
	}
	return 0
}

func prettyBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.2fG", float64(n)/float64(1<<30))
	}
	if n >= 1<<20 {
		return fmt.Sprintf("%.1fM", float64(n)/float64(1<<20))
	}
	return fmt.Sprintf("%dB", n)
}

func quotaOrInf(n int64) string {
	if n == 0 {
		return "∞"
	}
	return prettyBytes(n)
}

func userShow(args []string) int {
	var dbPath, email, baseURL string
	fs, ok := parseFlagSet("user show", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.StringVar(&email, "email", "", "")
		fs.StringVar(&baseURL, "base-url", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	c, ok := findClientOr(st, email)
	if !ok {
		return 1
	}
	fmt.Printf("id:         %d (inbound %d)\nuuid:       %s\ncreated:    %s\nquota:      %s / %s\nexpire:     %s\nip-limit:   %d\nspeed-limit: %d kbps\nsub token:  %s\n",
		c.ID, c.InboundID, c.UUID, c.CreatedAt.UTC().Format("2006-01-02"),
		prettyBytes(c.UpBytes+c.DownBytes), quotaOrInf(c.TotalBytes),
		expireStr(c), c.IPLimit, c.SpeedLimit, c.SubToken)
	if baseURL != "" && c.SubToken != "" {
		fmt.Printf("sub url:    %s\n", (&subs.Server{BaseURL: baseURL}).SubURL(c.SubToken))
	}
	return 0
}

func expireStr(c *store.Client) string {
	if c.ExpireAt == nil {
		return "—"
	}
	return c.ExpireAt.UTC().Format("2006-01-02")
}

func userSet(args []string) int {
	var dbPath, email, expireAt string
	var quotaGB float64
	var expireDays, ipLimit, speedKbps int
	fs, ok := parseFlagSet("user set", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.StringVar(&email, "email", "", "")
		fs.Float64Var(&quotaGB, "quota", -1, "GB, 0 = unlimited")
		fs.IntVar(&expireDays, "expire-days", -1, "")
		fs.StringVar(&expireAt, "expire-at", "", "")
		fs.IntVar(&ipLimit, "ip-limit", -1, "")
		fs.IntVar(&speedKbps, "speed", -1, "kbps")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	c, ok := findClientOr(st, email)
	if !ok {
		return 1
	}
	if quotaGB >= 0 {
		c.TotalBytes = gbToBytes(quotaGB)
	}
	if expireDays >= 0 {
		t := time.Now().UTC().Add(time.Duration(expireDays) * 24 * time.Hour)
		c.ExpireAt = &t
	}
	if expireAt != "" {
		t, err := time.Parse(time.RFC3339, expireAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "-expire-at must be RFC3339: %v\n", err)
			return 2
		}
		c.ExpireAt = &t
	}
	if ipLimit >= 0 {
		c.IPLimit = ipLimit
	}
	if speedKbps >= 0 {
		c.SpeedLimit = int64(speedKbps)
	}
	if err := st.UpdateClient(c); err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		return 1
	}
	logEvent(dbPath, "info", "user", fmt.Sprintf("updated %s (quota=%s, expire=%s)", c.Email, quotaOrInf(c.TotalBytes), expireStr(c)), "")
	fmt.Println("✅ updated", c.Email)
	return 0
}

func userToggle(verb string, args []string) int {
	var dbPath, email string
	fs, ok := parseFlagSet("user "+verb, args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.StringVar(&email, "email", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	c, ok := findClientOr(st, email)
	if !ok {
		return 1
	}
	enable := verb == "enable"
	if err := st.SetClientEnabled(c.ID, enable); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", verb, err)
		return 1
	}
	logEvent(dbPath, "info", "user", fmt.Sprintf("%s %s", verb+dSuffix(verb), c.Email), "")
	fmt.Printf("✅ %s %s\n", verb+dSuffix(verb), c.Email)
	return 0
}

func dSuffix(verb string) string {
	if strings.HasSuffix(verb, "e") {
		return "d"
	}
	return "ed"
}

func userReset(args []string) int {
	var dbPath, email string
	fs, ok := parseFlagSet("user reset", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.StringVar(&email, "email", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	c, ok := findClientOr(st, email)
	if !ok {
		return 1
	}
	up, down, err := st.ResetClientUsage(c.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset: %v\n", err)
		return 1
	}
	logEvent(dbPath, "info", "user", fmt.Sprintf("reset %s: was %s up / %s down", c.Email, prettyBytes(up), prettyBytes(down)), "")
	fmt.Printf("✅ %s reset (was %s)\n", c.Email, prettyBytes(up+down))
	return 0
}

func userRotate(args []string) int {
	var dbPath, email, baseURL string
	fs, ok := parseFlagSet("user rotate-token", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.StringVar(&email, "email", "", "")
		fs.StringVar(&baseURL, "base-url", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	c, ok := findClientOr(st, email)
	if !ok {
		return 1
	}
	token := subs.NewToken()
	if err := st.RotateSubToken(c.ID, token); err != nil {
		fmt.Fprintf(os.Stderr, "rotate: %v\n", err)
		return 1
	}
	logEvent(dbPath, "warn", "user", fmt.Sprintf("rotated sub token of %s", c.Email), "")
	fmt.Printf("✅ new token for %s: %s\n", c.Email, token)
	if baseURL != "" {
		fmt.Printf("sub url: %s\n", (&subs.Server{BaseURL: baseURL}).SubURL(token))
	}
	return 0
}

func userDelete(args []string) int {
	var dbPath, email string
	fs, ok := parseFlagSet("user delete", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
		fs.StringVar(&email, "email", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	c, ok := findClientOr(st, email)
	if !ok {
		return 1
	}
	if err := st.DeleteClient(c.ID); err != nil {
		fmt.Fprintf(os.Stderr, "delete: %v\n", err)
		return 1
	}
	logEvent(dbPath, "warn", "user", "deleted "+c.Email, "")
	fmt.Println("✅ deleted", c.Email)
	return 0
}

func userInbounds(args []string) int {
	var dbPath string
	fs, ok := parseFlagSet("user inbounds", args, func(fs *flag.FlagSet) {
		fs.StringVar(&dbPath, "db", "", "")
	})
	_ = fs
	if !ok {
		return 2
	}
	st, ok := openDBOr(dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	ins, err := st.ListInbounds()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		return 1
	}
	nodeNames := map[int64]string{}
	if nodes, err := st.ListNodes(); err == nil {
		for _, n := range nodes {
			nodeNames[n.ID] = n.Name
		}
	}
	fmt.Printf("%-4s %-20s %-8s %-6s %-14s %-8s %-8s %-12s %s\n", "ID", "REMARK", "PROTO", "PORT", "SECURITY", "ENABLED", "PRIVATE", "NODE", "HOST")
	for _, in := range ins {
		nodeName := "-"
		if in.NodeID != 0 {
			nodeName = nodeNames[in.NodeID]
			if nodeName == "" {
				nodeName = fmt.Sprintf("#%d", in.NodeID)
			}
		}
		priv := "-"
		if in.IsPrivate {
			priv = "yes"
		}
		fmt.Printf("%-4d %-20s %-8s %-6d %-14s %-8v %-8s %-12s %s\n", in.ID, in.Remark, in.Protocol, in.Port, in.Security, in.Enable, priv, nodeName, in.Host)
	}
	return 0
}
