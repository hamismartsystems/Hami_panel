package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func adminUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami admin create -db panel.db -user NAME [-password PASS]
  hami admin list   -db panel.db
  hami admin passwd -db panel.db -user NAME [-password PASS]
  hami admin delete -db panel.db -user NAME

If -password is omitted a strong one is generated and printed once.
Passwords are stored as PBKDF2-SHA256 with a per-account salt, never in clear.
`)
}

func adminCmd(args []string) int {
	if len(args) < 1 {
		adminUsage()
		return 2
	}
	switch args[0] {
	case "create":
		return adminCreate(args[1:])
	case "list":
		return adminList(args[1:])
	case "passwd", "password":
		return adminPasswd(args[1:])
	case "delete", "remove", "rm":
		return adminDelete(args[1:])
	}
	adminUsage()
	return 2
}

// genPassword returns 18 bytes of URL-safe randomness (~144 bits).
func genPassword() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// readPassword falls back to stdin when neither -password nor a TTY prompt is
// available, so the command stays usable from scripts.
func readPassword(flagValue string) (string, bool) {
	if flagValue != "" {
		return flagValue, false
	}
	st, err := os.Stdin.Stat()
	if err == nil && (st.Mode()&os.ModeCharDevice) == 0 {
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			if p := strings.TrimSpace(sc.Text()); p != "" {
				return p, false
			}
		}
	}
	return genPassword(), true
}

func openStore(path string) (*store.Store, int) {
	if path == "" {
		adminUsage()
		return nil, 2
	}
	st, err := store.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return nil, 1
	}
	return st, 0
}

func adminCreate(args []string) int {
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	user := fs.String("user", "", "")
	pass := fs.String("password", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *dbPath == "" || *user == "" {
		adminUsage()
		return 2
	}
	st, code := openStore(*dbPath)
	if st == nil {
		return code
	}
	defer st.Close()

	password, generated := readPassword(*pass)
	a, err := st.CreateAdmin(*user, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create admin: %v\n", err)
		return 1
	}
	_ = st.AddEvent("info", "cli", "created admin "+a.Username, "")
	fmt.Printf("admin created: %s\n", a.Username)
	if generated {
		fmt.Printf("password: %s\n", password)
		fmt.Fprintln(os.Stderr, "save it now — it is not stored anywhere in clear text")
	}
	return 0
}

func adminList(args []string) int {
	fs := flag.NewFlagSet("admin list", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *dbPath == "" {
		adminUsage()
		return 2
	}
	st, code := openStore(*dbPath)
	if st == nil {
		return code
	}
	defer st.Close()

	admins, err := st.ListAdmins()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list admins: %v\n", err)
		return 1
	}
	if len(admins) == 0 {
		fmt.Println("no admin account yet — hami admin create -db " + *dbPath + " -user NAME")
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSERNAME\tCREATED\tLAST LOGIN")
	for _, a := range admins {
		last := "never"
		if a.LastLogin != nil {
			last = a.LastLogin.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", a.ID, a.Username,
			a.CreatedAt.UTC().Format("2006-01-02"), last)
	}
	if err := w.Flush(); err != nil {
		return 1
	}
	return 0
}

func adminPasswd(args []string) int {
	fs := flag.NewFlagSet("admin passwd", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	user := fs.String("user", "", "")
	pass := fs.String("password", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *dbPath == "" || *user == "" {
		adminUsage()
		return 2
	}
	st, code := openStore(*dbPath)
	if st == nil {
		return code
	}
	defer st.Close()

	password, generated := readPassword(*pass)
	if err := st.SetAdminPassword(*user, password); err != nil {
		fmt.Fprintf(os.Stderr, "set password: %v\n", err)
		return 1
	}
	_ = st.AddEvent("warn", "cli", "password changed for admin "+*user, "sessions revoked")
	fmt.Printf("password changed for %s (all open sessions revoked)\n", *user)
	if generated {
		fmt.Printf("password: %s\n", password)
	}
	return 0
}

func adminDelete(args []string) int {
	fs := flag.NewFlagSet("admin delete", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	user := fs.String("user", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *dbPath == "" || *user == "" {
		adminUsage()
		return 2
	}
	st, code := openStore(*dbPath)
	if st == nil {
		return code
	}
	defer st.Close()

	if err := st.DeleteAdmin(*user); err != nil {
		fmt.Fprintf(os.Stderr, "delete admin: %v\n", err)
		return 1
	}
	_ = st.AddEvent("warn", "cli", "deleted admin "+*user, "")
	fmt.Printf("admin deleted: %s\n", *user)
	return 0
}
