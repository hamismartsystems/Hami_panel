package store

import (
	"errors"
	"fmt"
	"strings"
)

// UpdateInbound changes the parts of an inbound that can safely change
// on a live server.
//
// Protocol, transport and security are deliberately not among them: a
// different protocol is a different inbound, and quietly rewriting one
// would hand every existing customer a config that no longer matches
// the server. Reality keys have their own command, because rotating a
// key is a decision, not an edit.
//
// Changing the address or the port does change every customer's link.
// Anyone on a subscription picks that up on their next refresh; anyone
// who pasted a config once has to be sent a new one. The caller is
// expected to say so.
func (s *Store) UpdateInbound(in *Inbound) error {
	if in.ID == 0 {
		return errors.New("inbound id is required")
	}
	if in.Port <= 0 || in.Port > 65535 {
		return fmt.Errorf("port %d is not a port", in.Port)
	}
	if strings.TrimSpace(in.Host) == "" {
		return errors.New("an inbound needs an address for clients to dial")
	}

	// Two inbounds on one port would mean the second never starts.
	var clash int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM inbounds WHERE port = ? AND id <> ?`,
		in.Port, in.ID).Scan(&clash); err != nil {
		return err
	}
	if clash > 0 {
		return fmt.Errorf("port %d is already used by another inbound", in.Port)
	}

	res, err := s.db.Exec(`UPDATE inbounds SET remark=?, port=?, host=?, sni=?,
		fingerprint=?, path=?, xhttp_mode=?, header_type=?, flow=?, node_id=?,
		is_private=?, enable=? WHERE id=?`,
		in.Remark, in.Port, in.Host, in.SNI, in.Fingerprint, in.Path,
		in.XHTTPMode, in.HeaderType, in.Flow, in.NodeID,
		boolInt(in.IsPrivate), boolInt(in.Enable), in.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inbound %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

// UpdateInboundDest changes the Reality handshake target. It lives apart
// from the row above because it is stored with the secrets.
func (s *Store) UpdateInboundDest(id int64, dest string) error {
	sec, err := s.GetInboundSecret(id)
	if err != nil {
		return err
	}
	sec.InboundID = id
	sec.Dest = dest
	return s.SetInboundSecret(sec)
}
