package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Node statuses.
const (
	NodeUnknown  = "unknown"  // registered, never probed
	NodeHealthy  = "healthy"  // last probe OK
	NodeUnstable = "unstable" // probe OK but degraded (high load)
	NodeDown     = "down"     // probe failed
)

// Node is one remote server running the hami agent.
type Node struct {
	ID       int64
	Name     string
	Address  string // host:port of the agent endpoint
	APIKey   string
	LastSeen *time.Time
	Status   string
}

// NewAPIKey generates a fresh node API key: 192 bits, hex.
func NewAPIKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// CreateNode registers a node. If APIKey is empty one is generated.
func (s *Store) CreateNode(n *Node) error {
	if n.Name == "" || n.Address == "" {
		return errors.New("node name and address are required")
	}
	if n.APIKey == "" {
		n.APIKey = NewAPIKey()
	}
	if n.Status == "" {
		n.Status = NodeUnknown
	}
	res, err := s.db.Exec(`INSERT INTO nodes (name, address, api_key, status)
		VALUES (?,?,?,?)`, n.Name, n.Address, n.APIKey, n.Status)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

func scanNode(row scanner) (*Node, error) {
	var (
		n     Node
		seen  sql.NullString
		empty = ""
	)
	_ = empty
	err := row.Scan(&n.ID, &n.Name, &n.Address, &n.APIKey, &seen, &n.Status)
	if err != nil {
		return nil, err
	}
	if seen.Valid && seen.String != "" {
		if t, err := time.Parse(time.RFC3339, seen.String); err == nil {
			n.LastSeen = &t
		}
	}
	return &n, nil
}

const nodeColumns = `id, name, address, api_key, last_seen, status`

// GetNode looks a node up by id.
func (s *Store) GetNode(id int64) (*Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeColumns+` FROM nodes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node %d: %w", id, ErrNotFound)
	}
	return n, err
}

// GetNodeByName looks a node up by name.
func (s *Store) GetNodeByName(name string) (*Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeColumns+` FROM nodes WHERE name=?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node %q: %w", name, ErrNotFound)
	}
	return n, err
}

// ListNodes returns every registered node.
func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeColumns + ` FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// DeleteNode removes a node; its inbounds fall back to "this server".
func (s *Store) DeleteNode(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE inbounds SET node_id=0 WHERE node_id=?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("node %d: %w", id, ErrNotFound)
	}
	return tx.Commit()
}

// MarkNodeStatus stores the probe outcome.
func (s *Store) MarkNodeStatus(id int64, status string, seen time.Time) error {
	res, err := s.db.Exec(`UPDATE nodes SET status=?, last_seen=? WHERE id=?`,
		status, seen.UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("node %d: %w", id, ErrNotFound)
	}
	return nil
}

// LeastLoadedInbound picks the inbound that should receive the next new
// client (stage 3, auto distribution): enabled inbounds only, sitting on a
// node that is not "down" (unknown/unstable/local-node-0 stay eligible —
// exactly the same eligibility rule the subscription layer uses), then the
// one with the fewest clients; ties break by lowest inbound id so the result
// is deterministic.
func (s *Store) LeastLoadedInbound() (*Inbound, error) {
	rows, err := s.db.Query(`
		SELECT i.id, COUNT(c.id) AS n
		FROM inbounds i
		LEFT JOIN clients c ON c.inbound_id = i.id
		WHERE i.enable = 1
		  AND (i.node_id = 0 OR COALESCE(
		      (SELECT status FROM nodes WHERE id = i.node_id), 'unknown') != 'down')
		GROUP BY i.id
		ORDER BY n ASC, i.id ASC
		LIMIT 1`)
	if err != nil {
		return nil, err
	}
	var inID int64
	found := false
	for rows.Next() {
		if err := rows.Scan(&inID, new(int)); err != nil {
			rows.Close()
			return nil, err
		}
		found = true
	}
	rows.Close()
	if !found {
		return nil, ErrNotFound
	}
	return s.GetInbound(inID)
}

// FirstEnabledInboundOnNode returns the lowest-id enabled inbound of a node,
// used by `user create -node NAME` (explicit manual distribution).
func (s *Store) FirstEnabledInboundOnNode(nodeID int64) (*Inbound, error) {
	rows, err := s.db.Query(
		`SELECT id FROM inbounds WHERE node_id=? AND enable=1 ORDER BY id ASC LIMIT 1`, nodeID)
	if err != nil {
		return nil, err
	}
	var id int64
	found := false
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		found = true
	}
	rows.Close()
	if !found {
		return nil, ErrNotFound
	}
	return s.GetInbound(id)
}
