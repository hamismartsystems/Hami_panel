// Package traffic moves usage figures out of the running core and into
// the panel's own database.
//
// Until this existed the panel showed whatever the numbers were at the
// moment of import and never moved again: a quota could not deplete, an
// exhausted customer kept working, and every account looked offline. The
// core counts all of it already; nothing was reading it.
//
// The core is asked through its own command line rather than its gRPC
// API, which keeps protobuf and a gRPC stack out of this binary for what
// amounts to one call a minute.
package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// Sample is one customer's usage since the previous collection.
type Sample struct {
	Email  string
	Up     int64
	Down   int64
	Online bool
}

// Collector reads the core's statistics.
type Collector struct {
	// Bin is the xray binary; it speaks to the running core over the
	// loopback API.
	Bin string
	// API is host:port of the core's statistics endpoint.
	API string
}

// Query asks the core for per-user counters and resets them, so each call
// returns only what happened since the last one. Resetting is what makes
// the sum in the database authoritative: a restart of the core cannot
// double-count, and a missed run loses at most that interval.
func (c Collector) Query(ctx context.Context) ([]Sample, error) {
	out, err := c.run(ctx, "statsquery", "-pattern", "user>>>", "-reset")
	if err != nil {
		return nil, err
	}
	byEmail := map[string]*Sample{}
	for _, st := range out {
		// names look like: user>>>someone@example>>>traffic>>>uplink
		parts := strings.Split(st.Name, ">>>")
		if len(parts) < 4 || parts[0] != "user" {
			continue
		}
		email, kind := parts[1], parts[3]
		s := byEmail[email]
		if s == nil {
			s = &Sample{Email: email}
			byEmail[email] = s
		}
		switch kind {
		case "uplink":
			s.Up = st.Value
		case "downlink":
			s.Down = st.Value
		}
	}

	// Online counters must not be reset: they are a current state, not a
	// total, and zeroing them would make everyone look offline until they
	// opened a new connection.
	if online, err := c.run(ctx, "statsquery", "-pattern", "user>>>", ""); err == nil {
		for _, st := range online {
			parts := strings.Split(st.Name, ">>>")
			if len(parts) < 4 || parts[0] != "user" || parts[3] != "online" {
				continue
			}
			if st.Value <= 0 {
				continue
			}
			s := byEmail[parts[1]]
			if s == nil {
				s = &Sample{Email: parts[1]}
				byEmail[parts[1]] = s
			}
			s.Online = true
		}
	}

	list := make([]Sample, 0, len(byEmail))
	for _, s := range byEmail {
		list = append(list, *s)
	}
	return list, nil
}

type stat struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

func (c Collector) run(ctx context.Context, args ...string) ([]stat, error) {
	full := append([]string{"api"}, args[0])
	full = append(full, "--server="+c.API)
	for _, a := range args[1:] {
		if a != "" {
			full = append(full, a)
		}
	}
	cmd := exec.CommandContext(ctx, c.Bin, full...)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("asking the core for statistics: %w", err)
	}
	// The core prints either {"stat":[...]} or an empty object when it
	// has nothing to report yet.
	var doc struct {
		Stat []stat `json:"stat"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the core's statistics were not json: %w", err)
	}
	return doc.Stat, nil
}

// Result is what one collection changed.
type Result struct {
	Matched  int
	Unknown  []string
	AddedUp  int64
	AddedDn  int64
	Online   int
	Disabled []string
	// Expired names the clients whose term ended during or before this
	// round. They are switched off for the same reason a drained client
	// is: the panel has to be the thing that decides who is served, and
	// an expiry date that only lives in the panel does not stop a
	// customer who saved the link months ago.
	Expired []string
	// Activated lists accounts whose subscription clock just started
	// because this was their first connection.
	Activated []string
}

// Apply folds a round of samples into the database: usage is added, the
// last-seen stamp moves for anyone who passed traffic, and clients that
// have crossed their quota or their end date are switched off.
//
// Switching off here is deliberate. A quota that is only displayed is not
// a quota, and the core has no idea what a customer paid for.
//
// Usage is written with a targeted update, never by writing the whole row
// back: this function runs in its own process, minutes apart from the
// panel, and a whole-row write would roll back anything the operator
// changed in between.
func Apply(st *store.Store, samples []Sample, now time.Time) (Result, error) {
	var res Result
	clients, err := st.ListAllClients()
	if err != nil {
		return res, err
	}
	byEmail := map[string]store.Client{}
	for _, c := range clients {
		byEmail[strings.ToLower(c.Email)] = c
	}

	for _, s := range samples {
		c, ok := byEmail[strings.ToLower(s.Email)]
		if !ok {
			// A counter for somebody the panel does not know about is
			// worth reporting: it usually means the core is running a
			// config the panel did not write.
			res.Unknown = append(res.Unknown, s.Email)
			continue
		}
		res.Matched++
		if s.Online {
			res.Online++
		}
		moved := s.Up > 0 || s.Down > 0
		if moved || s.Online {
			t := now.UTC()
			c.LastSeen = &t
		}
		// First connection of an account sold as "month from first
		// use": stamp its expiry now, counting the advertised days from
		// this moment. Afterwards activate_days reads zero, so this can
		// never fire twice.
		if (moved || s.Online) && c.ActivateDays > 0 && c.ExpireAt == nil {
			t := now.UTC().Add(time.Duration(c.ActivateDays) * 24 * time.Hour)
			c.ExpireAt = &t
			res.Activated = append(res.Activated, c.Email)
			c.ActivateDays = 0
			_ = st.AddEvent("info", "traffic",
				"subscription clock started for "+c.Email, "")
			if err := st.UpdateClient(&c); err != nil {
				return res, fmt.Errorf("%s: %w", c.Email, err)
			}
		}
		if moved {
			// A disabled client may still be in the running config for
			// a moment; counting its bytes without switching it on
			// again keeps the operator's decision final.
			if err := st.AddUsage(c.ID, s.Up, s.Down, c.LastSeen); err != nil {
				return res, fmt.Errorf("%s: %w", c.Email, err)
			}
			res.AddedUp += s.Up
			res.AddedDn += s.Down
		} else if s.Online {
			if err := st.AddUsage(c.ID, 0, 0, c.LastSeen); err != nil {
				return res, fmt.Errorf("%s: %w", c.Email, err)
			}
		}
		if !c.Enable {
			continue
		}
		// Re-read: the counters just written, and any edit the operator
		// made since this round started, are what decides here.
		fresh, err := st.GetClient(c.ID)
		if err != nil {
			return res, fmt.Errorf("%s: %w", c.Email, err)
		}
		if fresh.ActiveNow(now) {
			continue
		}
		who := strings.ToLower(strings.TrimSpace(s.Email))
		isQuota := fresh.TotalBytes > 0 && fresh.UpBytes+fresh.DownBytes >= fresh.TotalBytes
		if err := st.SetClientEnabled(c.ID, false); err != nil {
			return res, fmt.Errorf("%s: disabling (%s): %w", c.Email, fresh.WhyInactive(now), err)
		}
		switch {
		case isQuota:
			res.Disabled = append(res.Disabled, who)
		default:
			res.Expired = append(res.Expired, who)
		}
	}

	// A client that used nothing at all is still out of term as soon as
	// its date passes: catch those on every round, not only on use.
	for _, c := range clients {
		if !c.Enable || c.ExpireAt == nil {
			continue
		}
		if now.Before(c.ExpireAt.UTC()) {
			continue
		}
		if err := st.SetClientEnabled(c.ID, false); err != nil {
			return res, fmt.Errorf("%s: disabling (expired): %w", c.Email, err)
		}
		res.Expired = append(res.Expired, strings.ToLower(strings.TrimSpace(c.Email)))
	}
	return res, nil
}

// OnlineWithin reports whether a client counts as online, given how long
// ago it last moved traffic. The window must be comfortably longer than
// the collection interval, or everyone blinks offline between runs.
func OnlineWithin(c store.Client, window time.Duration, now time.Time) bool {
	if c.LastSeen == nil {
		return false
	}
	return now.Sub(*c.LastSeen) <= window
}
