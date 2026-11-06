package main

import (
	"os"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/actlog"
)

// cliServices lists the subcommands whose runs are written to the action log.
// Read-only and periodic commands are left out so the log stays readable.
var cliServices = map[string]string{
	"admin": "hami-cli", "user": "hami-cli", "inbound": "hami-cli", "node": "hami-cli",
	"import": "hami-cli", "restore": "hami-cli", "backup": "hami-cli", "template": "hami-cli",
	"reality": "hami-cli", "upgrade": "hami-cli",
	"bot": "hami-bot", "web": "hami-panel", "sub": "hami-sub", "notice": "hami-notice",
}

// cliLog records the subcommand and its verb (for example "admin passwd").
// Flags are never logged because they can carry secrets.
func cliLog() {
	if len(os.Args) < 2 {
		return
	}
	svc, ok := cliServices[os.Args[1]]
	if !ok {
		return
	}
	msg := os.Args[1]
	if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
		msg += " " + os.Args[2]
	}
	actlog.Log(svc, "command: "+msg)
}
