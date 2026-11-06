package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// This file holds what a bulk sale needs on top of the ordinary bot
// tables: which private inbound a reseller's accounts are placed on, and
// which account each slice of a bulk order became. Recording the slices
// one by one is what lets a half-finished delivery be resumed without
// creating the same account twice.

// OrderClient is one account produced by an order.
type OrderClient struct {
	OrderID  int64
	Seq      int // 1-based position inside the order
	ClientID int64
	Email    string
}

// SetResellerInbound points a reseller's future accounts at a private
// inbound. Zero clears it.
func (s *Store) SetResellerInbound(telegramID, inboundID int64) error {
	res, err := s.db.Exec(`UPDATE bot_users SET reseller_inbound_id = ?
		WHERE telegram_id = ?`, inboundID, telegramID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("telegram id %d: %w", telegramID, ErrNotFound)
	}
	return nil
}

// ResellerInboundID returns the private inbound of a reseller, or zero
// when none has been set up yet.
func (s *Store) ResellerInboundID(telegramID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT reseller_inbound_id FROM bot_users
		WHERE telegram_id = ?`, telegramID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// AddOrderClient records that slice seq of an order became this account.
// A second record for the same slice is refused by the primary key, so a
// retry cannot quietly double-count an account.
func (s *Store) AddOrderClient(orderID int64, seq int, clientID int64, email string) error {
	_, err := s.db.Exec(`INSERT INTO bot_order_clients (order_id, seq, client_id, email)
		VALUES (?,?,?,?)`, orderID, seq, clientID, email)
	return err
}

// OrderClients lists the accounts an order has produced so far, in order.
func (s *Store) OrderClients(orderID int64) ([]OrderClient, error) {
	rows, err := s.db.Query(`SELECT order_id, seq, client_id, email
		FROM bot_order_clients WHERE order_id = ? ORDER BY seq`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderClient
	for rows.Next() {
		var c OrderClient
		if err := rows.Scan(&c.OrderID, &c.Seq, &c.ClientID, &c.Email); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
