package web

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/xray"
)

// liveSampler polls the core's per-user counters every few seconds WITHOUT
// resetting them, so the gap between two reads is each customer's live
// speed — the up/down line other panels draw under a user's name.
//
// The minute collector (hami traffic collect) owns counter resets; this
// watcher never resets. When a reset lands between two reads the counter
// drops, which looks like a huge negative delta; the fresh counter value
// itself is then the best estimate of that interval's traffic, which keeps
// the displayed rate honest instead of flashing garbage.
type liveSampler struct {
	Bin string
	API string

	mu     sync.RWMutex
	prev   map[string][2]int64 // email -> cumulative {up, down}
	prevAt time.Time
	up     map[string]int64 // email -> bytes/sec
	down   map[string]int64 // email -> bytes/sec
}

func newLiveSampler(bin, api string) *liveSampler {
	return &liveSampler{Bin: bin, API: api,
		prev: map[string][2]int64{}, up: map[string]int64{}, down: map[string]int64{}}
}

// findXrayBin locates the core's binary so the sampler can ask it for
// statistics. The traffic timer passes it explicitly; the web process has
// no flag, so reuse the environment, the well-known install paths, then
// PATH. No binary means no live rates — the panel works regardless.
func findXrayBin() string {
	if b := os.Getenv("XRAY_BIN"); b != "" {
		if _, err := os.Stat(b); err == nil {
			return b
		}
	}
	for _, b := range []string{
		"/usr/local/x-ui/bin/xray-linux-amd64",
		"/usr/local/x-ui/bin/xray",
		"/usr/local/xray/xray",
		"/usr/local/bin/xray",
	} {
		if _, err := os.Stat(b); err == nil {
			return b
		}
	}
	if b, err := exec.LookPath("xray"); err == nil {
		return b
	}
	return ""
}

// start runs the sampler in the background. A missing binary or API is
// not an error: it simply leaves every rate at zero.
func (ls *liveSampler) start() {
	if ls == nil || ls.Bin == "" || ls.API == "" {
		return
	}
	go func() {
		ls.tick() // warm-up read, so the first live tick has a baseline
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for range t.C {
			ls.tick()
		}
	}()
}

func (ls *liveSampler) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := ls.query(ctx)
	if err != nil {
		return
	}
	now := time.Now()

	ls.mu.Lock()
	defer ls.mu.Unlock()
	dt := now.Sub(ls.prevAt).Seconds()
	if dt <= 0 {
		dt = 3
	}
	up, down := map[string]int64{}, map[string]int64{}
	for email, v := range cur {
		p, ok := ls.prev[email]
		if !ok {
			continue // brand-new counter: needs one more tick for a baseline
		}
		du := v[0] - p[0]
		if du < 0 {
			du = v[0] // a collector reset landed between the two reads
		}
		dd := v[1] - p[1]
		if dd < 0 {
			dd = v[1]
		}
		up[email] = int64(float64(du) / dt)
		down[email] = int64(float64(dd) / dt)
	}
	ls.prev, ls.prevAt, ls.up, ls.down = cur, now, up, down
}

// rates returns the current up/down speed (bytes/sec) of one user, zero
// when idle or unknown.
func (ls *liveSampler) rates(email string) (int64, int64) {
	if ls == nil {
		return 0, 0
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.up[email], ls.down[email]
}

func (ls *liveSampler) query(ctx context.Context) (map[string][2]int64, error) {
	cmd := exec.CommandContext(ctx, ls.Bin,
		"api", "statsquery", "--server="+ls.API, "-pattern", "user>>>")
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Stat []struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	m := map[string][2]int64{}
	for _, st := range doc.Stat {
		// names look like: user>>>someone@example>>>traffic>>>uplink
		parts := strings.Split(st.Name, ">>>")
		if len(parts) < 4 || parts[0] != "user" {
			continue
		}
		v := m[parts[1]]
		switch parts[3] {
		case "uplink":
			v[0] = st.Value
		case "downlink":
			v[1] = st.Value
		default:
			continue
		}
		m[parts[1]] = v
	}
	return m, nil
}

// liveAPIAddr is the core's statistics endpoint on the loopback.
func liveAPIAddr() string {
	return "127.0.0.1:" + strconv.Itoa(xray.StatsAPIPort)
}
