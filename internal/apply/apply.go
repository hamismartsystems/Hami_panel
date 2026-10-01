// Package apply pushes the panel's data into the running core.
//
// Without this the panel is a pretty database: adding a customer changes
// a row and nothing else, so the config they were just sold does not
// work until somebody runs two commands by hand. That is fine while one
// person clicks buttons and watches; it is not fine the moment a bot
// takes money and hands out a config at three in the morning.
//
// Restarting the core drops live connections, so changes are coalesced:
// a burst of a hundred new users costs one restart, not a hundred.
package apply

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

// Applier regenerates the core's config and restarts it.
type Applier struct {
	Store *store.Store
	// ConfigPath is where the generated config is written. Empty
	// disables the whole mechanism, which is what tests and a
	// panel running without a local core want.
	ConfigPath string
	// Reload is the command that makes the core pick the config up.
	Reload []string
	// Debounce is how long to wait for more changes before acting.
	Debounce time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	pending bool
	last    error
	lastAt  time.Time
}

// Enabled reports whether this applier will do anything.
func (a *Applier) Enabled() bool { return a != nil && a.ConfigPath != "" }

// Schedule asks for the core to be brought up to date soon. It returns
// immediately: the caller is usually answering an HTTP request and
// should not wait on a service restart.
func (a *Applier) Schedule() {
	if !a.Enabled() {
		return
	}
	d := a.Debounce
	if d <= 0 {
		d = 2 * time.Second
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending = true
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(d, func() {
		if err := a.Now(); err != nil {
			// The operator has to be able to find out that the core is
			// behind the panel, so it goes in the event log too.
			_ = a.Store.AddEvent("error", "apply",
				"the core could not be brought up to date", err.Error())
		}
	})
}

// Now regenerates and reloads immediately.
func (a *Applier) Now() error {
	if !a.Enabled() {
		return nil
	}
	a.mu.Lock()
	a.pending = false
	a.mu.Unlock()

	endpoints, err := xray.EndpointsFromStore(a.Store)
	if err != nil {
		return a.record(fmt.Errorf("reading inbounds: %w", err))
	}
	if len(endpoints) == 0 {
		// Writing a config with no inbounds would take everyone offline.
		// Leaving the old one in place is the safer failure.
		return a.record(fmt.Errorf("refusing to write a config with no enabled inbound"))
	}
	cfg, err := xray.Build(endpoints)
	if err != nil {
		return a.record(fmt.Errorf("building the config: %w", err))
	}

	// Write beside the target and rename, so a crash midway cannot leave
	// the core with half a config to read on its next start.
	tmp := a.ConfigPath + ".new"
	if err := os.WriteFile(tmp, cfg, 0o600); err != nil {
		return a.record(fmt.Errorf("writing the config: %w", err))
	}
	if err := os.Rename(tmp, a.ConfigPath); err != nil {
		_ = os.Remove(tmp)
		return a.record(fmt.Errorf("replacing the config: %w", err))
	}

	if len(a.Reload) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, a.Reload[0], a.Reload[1:]...).CombinedOutput()
		if err != nil {
			return a.record(fmt.Errorf("reloading the core: %w: %s", err, out))
		}
	}

	clients := 0
	for _, e := range endpoints {
		clients += len(e.Clients)
	}
	_ = a.Store.AddEvent("info", "apply",
		fmt.Sprintf("core updated: %d inbound(s), %d client(s)", len(endpoints), clients), "")
	return a.record(nil)
}

func (a *Applier) record(err error) error {
	a.mu.Lock()
	a.last, a.lastAt = err, time.Now()
	a.mu.Unlock()
	return err
}

// Status reports the last attempt, for the dashboard to show when the
// core has fallen behind.
func (a *Applier) Status() (pending bool, lastErr error, at time.Time) {
	if a == nil {
		return false, nil, time.Time{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending, a.last, a.lastAt
}
