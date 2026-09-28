package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a lookup finds nothing.
var ErrNotFound = errors.New("not found")

// clientColumns is the canonical column list for client scans.
const clientColumns = `id, inbound_id, uuid, password, method,
	ss_password, email, enable, total_bytes, up_bytes, down_bytes, expire_at,
	ip_limit, speed_limit, sub_token, created_at`

// ClientBySubToken finds the client holding this subscription token.
// An empty token never matches.
func (s *Store) ClientBySubToken(token string) (*Client, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	c, err := scanClient(s.db.QueryRow(
		`SELECT `+clientColumns+` FROM clients WHERE sub_token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// GetClientByEmail looks a client up by its owner label.
func (s *Store) GetClientByEmail(email string) (*Client, error) {
	c, err := scanClient(s.db.QueryRow(
		`SELECT `+clientColumns+` FROM clients WHERE email = ? ORDER BY id LIMIT 1`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ListAllClients returns every client across all inbounds.
func (s *Store) ListAllClients() ([]Client, error) {
	rows, err := s.db.Query(`SELECT ` + clientColumns + ` FROM clients ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Client{}
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpdateClient writes the mutable fields of an existing client.
func (s *Store) UpdateClient(c *Client) error {
	if c.ID == 0 {
		return errors.New("client id is required")
	}
	var expire interface{}
	if c.ExpireAt != nil {
		expire = c.ExpireAt.UTC().Format(time.RFC3339)
	}
	res, err := s.db.Exec(`UPDATE clients SET uuid=?, password=?, method=?,
		ss_password=?, email=?, enable=?, total_bytes=?, up_bytes=?, down_bytes=?,
		expire_at=?, ip_limit=?, speed_limit=? WHERE id=?`,
		c.UUID, c.Password, c.Method, c.SSPassword, c.Email, boolInt(c.Enable),
		c.TotalBytes, c.UpBytes, c.DownBytes, expire, c.IPLimit, c.SpeedLimit, c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("client %d: %w", c.ID, ErrNotFound)
	}
	return nil
}

// RotateSubToken replaces the client's subscription token. The old link
// stops working the moment this is written.
func (s *Store) RotateSubToken(id int64, token string) error {
	if token == "" {
		return errors.New("token must not be empty")
	}
	res, err := s.db.Exec(`UPDATE clients SET sub_token=? WHERE id=?`, token, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("client %d: %w", id, ErrNotFound)
	}
	return nil
}

// ResetClientUsage zeroes the up/down counters. Past traffic samples stay
// in the traffic table as history — only "current period" usage resets.
func (s *Store) ResetClientUsage(id int64) (up, down int64, err error) {
	c, err := s.GetClient(id)
	if err != nil {
		return 0, 0, fmt.Errorf("client %d: %w", id, ErrNotFound)
	}
	if _, err := s.db.Exec(`UPDATE clients SET up_bytes=0, down_bytes=0 WHERE id=?`, id); err != nil {
		return 0, 0, err
	}
	return c.UpBytes, c.DownBytes, nil
}
