package importer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// Action is what the plan intends to do with one record.
type Action string

const (
	Create Action = "create"
	Exists Action = "exists" // already imported; left alone
)

// PlannedInbound is one inbound and its clients, decided against what the
// destination already holds.
type PlannedInbound struct {
	Action   Action
	Reason   string
	Source   string
	Inbound  store.Inbound
	Secret   store.InboundSecret
	ExistsAs int64 // destination id when Action is Exists
	Clients  []PlannedClient
}

// PlannedClient is one client and what will happen to it.
type PlannedClient struct {
	Action Action
	Reason string
	Client store.Client
}

// Plan is the whole import, decided but not executed.
type Plan struct {
	Snapshot *Snapshot
	Inbounds []PlannedInbound
	// Conflicts are things that cannot be imported as they stand and
	// would need the operator to intervene.
	Conflicts []string
}

// Counts summarises a plan.
func (p *Plan) Counts() (newInbounds, newClients, existingInbounds, existingClients int) {
	for _, in := range p.Inbounds {
		if in.Action == Create {
			newInbounds++
		} else {
			existingInbounds++
		}
		for _, c := range in.Clients {
			if c.Action == Create {
				newClients++
			} else {
				existingClients++
			}
		}
	}
	return
}

// BuildPlan decides what to do without touching anything.
//
// Matching is deliberately conservative. An inbound counts as already
// imported when protocol, port and remark all agree, and a client when it
// sits on that inbound with the same identity. Anything ambiguous becomes
// a conflict for a human to settle rather than a guess.
func BuildPlan(st *store.Store, snap *Snapshot) (*Plan, error) {
	existing, err := st.ListInbounds()
	if err != nil {
		return nil, fmt.Errorf("reading the destination: %w", err)
	}
	byKey := map[string]store.Inbound{}
	byPort := map[int][]store.Inbound{}
	for _, in := range existing {
		byKey[inboundKey(in)] = in
		byPort[in.Port] = append(byPort[in.Port], in)
	}

	p := &Plan{Snapshot: snap}
	// Ports claimed by this plan, so two imported inbounds cannot collide
	// with each other either.
	claimed := map[int]string{}

	for _, imp := range snap.Inbounds {
		pi := PlannedInbound{
			Action: Create, Source: imp.SourceRef,
			Inbound: imp.Inbound, Secret: imp.Secret,
		}

		if found, ok := byKey[inboundKey(imp.Inbound)]; ok {
			pi.Action, pi.ExistsAs = Exists, found.ID
			pi.Reason = "an inbound with the same protocol, port and name is already here"
		} else if others := byPort[imp.Inbound.Port]; len(others) > 0 {
			names := make([]string, 0, len(others))
			for _, o := range others {
				names = append(names, fmt.Sprintf("%q", o.Remark))
			}
			p.Conflicts = append(p.Conflicts, fmt.Sprintf(
				"%s wants port %d, which %s already uses — rename or move one of them",
				imp.SourceRef, imp.Inbound.Port, strings.Join(names, ", ")))
			continue
		} else if prev, ok := claimed[imp.Inbound.Port]; ok {
			p.Conflicts = append(p.Conflicts, fmt.Sprintf(
				"%s and %s both want port %d", prev, imp.SourceRef, imp.Inbound.Port))
			continue
		}
		claimed[imp.Inbound.Port] = imp.SourceRef

		// Clients already on the destination inbound.
		have := map[string]bool{}
		haveEmail := map[string]bool{}
		if pi.Action == Exists {
			cur, err := st.ListClientsOf(pi.ExistsAs)
			if err != nil {
				return nil, fmt.Errorf("reading existing clients: %w", err)
			}
			for _, c := range cur {
				have[clientKey(c)] = true
				haveEmail[strings.ToLower(c.Email)] = true
			}
		}

		seen := map[string]bool{}
		for _, c := range imp.Clients {
			pc := PlannedClient{Action: Create, Client: c}
			switch {
			case have[clientKey(c)]:
				pc.Action, pc.Reason = Exists, "already imported"
			case haveEmail[strings.ToLower(c.Email)]:
				p.Conflicts = append(p.Conflicts, fmt.Sprintf(
					"%s: a different client named %q is already on this inbound",
					imp.SourceRef, c.Email))
				continue
			case seen[clientKey(c)]:
				p.Conflicts = append(p.Conflicts, fmt.Sprintf(
					"%s: the source lists %q twice", imp.SourceRef, c.Email))
				continue
			}
			seen[clientKey(c)] = true
			pi.Clients = append(pi.Clients, pc)
		}
		p.Inbounds = append(p.Inbounds, pi)
	}

	sort.SliceStable(p.Inbounds, func(i, j int) bool {
		return p.Inbounds[i].Inbound.Port < p.Inbounds[j].Inbound.Port
	})
	return p, nil
}

func inboundKey(in store.Inbound) string {
	return strings.ToLower(fmt.Sprintf("%s|%d|%s", in.Protocol, in.Port, in.Remark))
}

func clientKey(c store.Client) string {
	id := c.UUID
	if id == "" {
		id = c.Password
	}
	if id == "" {
		id = c.SSPassword
	}
	return strings.ToLower(c.Email + "|" + id)
}

// Result is what actually happened.
type Result struct {
	BackupPath      string
	CreatedInbounds int
	CreatedClients  int
	Skipped         int
	Verified        bool
}

// Apply writes the plan into HAMI's database.
//
// Before a single row is written the destination file is copied aside, and
// the path is reported whatever happens, so a bad import is one file copy
// away from being undone. After writing, everything is read back and
// compared against the plan; a mismatch is reported rather than assumed
// away.
func Apply(st *store.Store, dbPath string, p *Plan) (*Result, error) {
	res := &Result{}

	if dbPath != "" {
		bak, err := backupFile(dbPath)
		if err != nil {
			return nil, fmt.Errorf("could not back up the destination first: %w", err)
		}
		res.BackupPath = bak
	}

	for i := range p.Inbounds {
		pi := &p.Inbounds[i]

		id := pi.ExistsAs
		if pi.Action == Create {
			in := pi.Inbound
			if err := st.CreateInbound(&in); err != nil {
				return res, fmt.Errorf("%s: %w (nothing after this was imported; "+
					"the database before this run is at %s)", pi.Source, err, res.BackupPath)
			}
			id = in.ID
			pi.ExistsAs = in.ID
			res.CreatedInbounds++

			if pi.Secret.PrivateKey != "" || pi.Secret.Dest != "" || pi.Secret.Listen != "" {
				sec := pi.Secret
				sec.InboundID = in.ID
				if err := st.SetInboundSecret(sec); err != nil {
					return res, fmt.Errorf("%s: saving the reality key: %w", pi.Source, err)
				}
			}
		}

		for _, pc := range pi.Clients {
			if pc.Action != Create {
				res.Skipped++
				continue
			}
			c := pc.Client
			c.InboundID = id
			if err := st.CreateClient(&c); err != nil {
				return res, fmt.Errorf("%s: client %q: %w (the database before this run "+
					"is at %s)", pi.Source, c.Email, err, res.BackupPath)
			}
			// New clients are created enabled. A customer who was switched
			// off in the old panel — unpaid, suspended — must stay off
			// here, so put them back the way they were.
			if !pc.Client.Enable {
				if err := st.SetClientEnabled(c.ID, false); err != nil {
					return res, fmt.Errorf("%s: client %q was disabled in the source "+
						"and could not be disabled here: %w (the database before this "+
						"run is at %s)", pi.Source, c.Email, err, res.BackupPath)
				}
			}
			res.CreatedClients++
		}
	}

	if err := verify(st, p); err != nil {
		return res, fmt.Errorf("the import finished but reading it back disagreed: %w "+
			"(the database before this run is at %s)", err, res.BackupPath)
	}
	res.Verified = true
	return res, nil
}

// verify re-reads the destination and checks every planned record landed
// with the values it was supposed to have.
func verify(st *store.Store, p *Plan) error {
	for _, pi := range p.Inbounds {
		if pi.ExistsAs == 0 {
			return fmt.Errorf("%s has no id after import", pi.Source)
		}
		got, err := st.GetInbound(pi.ExistsAs)
		if err != nil {
			return fmt.Errorf("%s: %w", pi.Source, err)
		}
		if pi.Action == Create {
			if got.Port != pi.Inbound.Port || got.Protocol != pi.Inbound.Protocol {
				return fmt.Errorf("%s: stored as %s:%d, expected %s:%d", pi.Source,
					got.Protocol, got.Port, pi.Inbound.Protocol, pi.Inbound.Port)
			}
			if got.Security != pi.Inbound.Security || got.SNI != pi.Inbound.SNI {
				return fmt.Errorf("%s: security or sni did not survive the write", pi.Source)
			}
		}

		clients, err := st.ListClientsOf(pi.ExistsAs)
		if err != nil {
			return fmt.Errorf("%s: %w", pi.Source, err)
		}
		index := map[string]store.Client{}
		for _, c := range clients {
			index[clientKey(c)] = c
		}
		for _, pc := range pi.Clients {
			got, ok := index[clientKey(pc.Client)]
			if !ok {
				return fmt.Errorf("%s: client %q is missing after the write",
					pi.Source, pc.Client.Email)
			}
			if pc.Action != Create {
				continue
			}
			if got.TotalBytes != pc.Client.TotalBytes {
				return fmt.Errorf("%s: client %q quota is %d, expected %d",
					pi.Source, pc.Client.Email, got.TotalBytes, pc.Client.TotalBytes)
			}
			if got.UpBytes != pc.Client.UpBytes || got.DownBytes != pc.Client.DownBytes {
				return fmt.Errorf("%s: client %q usage did not survive the write",
					pi.Source, pc.Client.Email)
			}
			if !sameTime(got.ExpireAt, pc.Client.ExpireAt) {
				return fmt.Errorf("%s: client %q expiry did not survive the write",
					pi.Source, pc.Client.Email)
			}
			if got.Enable != pc.Client.Enable {
				return fmt.Errorf("%s: client %q was %s in the source but %s here",
					pi.Source, pc.Client.Email,
					onOff(pc.Client.Enable), onOff(got.Enable))
			}
			if got.SubToken != pc.Client.SubToken {
				return fmt.Errorf("%s: client %q subscription id did not survive the write",
					pi.Source, pc.Client.Email)
			}
			if got.IPLimit != pc.Client.IPLimit {
				return fmt.Errorf("%s: client %q device limit did not survive the write",
					pi.Source, pc.Client.Email)
			}
		}
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func sameTime(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Unix() == b.Unix()
	}
}

func backupFile(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()

	dst := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.before-import-%s",
		filepath.Base(path), time.Now().UTC().Format("20060102-150405")))
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	return dst, nil
}
