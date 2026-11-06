// Package actlog appends one line per action to the server action log.
// Line format matches /root/hami/actlog/actlog.sh: Tehran time, UTC time, host, service, message.
// Messages must never contain tokens, passwords, links or message bodies.
package actlog

import (
	"fmt"
	"os"
	"sync"
	"time"
)

const logPath = "/var/log/hami/actions.log"

var (
	mu     sync.Mutex
	tehran = time.FixedZone("Tehran", 3*3600+30*60)
)

// Log appends one action. Failures to write are ignored so logging never breaks a service.
func Log(service, msg string) {
	now := time.Now()
	line := fmt.Sprintf("%s Tehran | %sZ | FI | %s | %s\n",
		now.In(tehran).Format("2006-01-02 15:04:05"),
		now.UTC().Format("15:04:05"),
		service, oneLine(msg))
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

func oneLine(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '\n' || c == '\r' {
			b[i] = ' '
		}
	}
	return string(b)
}
