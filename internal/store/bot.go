package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds everything the sales bot needs: who is talking to it,
// what they have in their wallet, and what they have ordered. It lives
// in the same database as the customers so an order and the account it
// produced can never drift apart.

// ErrInsufficientFunds is returned when a wallet cannot cover a charge.
var ErrInsufficientFunds = errors.New("not enough balance")

// BotUser is someone who has talked to the bot.
type BotUser struct {
	TelegramID int64
	Username   string
	FirstName  string
	Balance    int64 // toman
	IsReseller bool
	CreatedAt  time.Time
}

// Order is one purchase, from the moment it is quoted to delivery.
type Order struct {
	ID         int64
	TelegramID int64
	Plan       string // "monthly" or "forever"
	GB         int
	Price      int64 // what this buyer pays, after any discount
	ListPrice  int64 // what a normal customer would pay
	Status     string
	PaidFrom   string // "wallet" or "card"
	ClientID   int64  // the account produced, once delivered
	Email      string
	CreatedAt  time.Time
}

// Order states. An order only ever moves forward.
const (
	OrderPending   = "pending"   // quoted, not paid
	OrderAwaiting  = "awaiting"  // receipt sent, waiting for the operator
	OrderPaid      = "paid"      // approved, not yet delivered
	OrderDelivered = "delivered" // the account exists
	OrderRejected  = "rejected"
)

// UpsertBotUser records someone the bot has heard from, without
// disturbing a balance or reseller flag that is already set.
func (s *Store) UpsertBotUser(id int64, username, first string) (*BotUser, error) {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO bot_users (telegram_id, username, first_name,
		balance, is_reseller, created_at) VALUES (?,?,?,0,0,?)
		ON CONFLICT(telegram_id) DO UPDATE SET username=excluded.username,
		first_name=excluded.first_name`, id, username, first, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return s.BotUser(id)
}

// BotUser loads one, or nil when the bot has never heard from them.
func (s *Store) BotUser(id int64) (*BotUser, error) {
	var (
		u        BotUser
		reseller int
		created  string
	)
	err := s.db.QueryRow(`SELECT telegram_id, username, first_name, balance,
		is_reseller, created_at FROM bot_users WHERE telegram_id = ?`, id).
		Scan(&u.TelegramID, &u.Username, &u.FirstName, &u.Balance, &reseller, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.IsReseller = reseller != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &u, nil
}

// SetReseller marks an account as paying the reseller rate.
func (s *Store) SetReseller(id int64, yes bool) error {
	res, err := s.db.Exec(`UPDATE bot_users SET is_reseller = ? WHERE telegram_id = ?`,
		boolInt(yes), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("telegram id %d: %w", id, ErrNotFound)
	}
	return nil
}

// ListResellers is for the operator to see who is on the reduced rate.
func (s *Store) ListResellers() ([]BotUser, error) {
	rows, err := s.db.Query(`SELECT telegram_id, username, first_name, balance,
		is_reseller, created_at FROM bot_users WHERE is_reseller = 1 ORDER BY telegram_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotUser
	for rows.Next() {
		var u BotUser
		var reseller int
		var created string
		if err := rows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &u.Balance,
			&reseller, &created); err != nil {
			return nil, err
		}
		u.IsReseller = reseller != 0
		u.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, u)
	}
	return out, rows.Err()
}

// WalletAdd moves a balance by a signed amount and returns the new one.
// A charge that would take the balance below zero is refused rather than
// silently clamped, because that would hand out a config nobody paid for.
func (s *Store) WalletAdd(id int64, delta int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var balance int64
	if err := tx.QueryRow(`SELECT balance FROM bot_users WHERE telegram_id = ?`, id).
		Scan(&balance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("telegram id %d: %w", id, ErrNotFound)
		}
		return 0, err
	}
	if balance+delta < 0 {
		return balance, ErrInsufficientFunds
	}
	balance += delta
	if _, err := tx.Exec(`UPDATE bot_users SET balance = ? WHERE telegram_id = ?`,
		balance, id); err != nil {
		return 0, err
	}
	return balance, tx.Commit()
}

// CreateOrder records a quote.
func (s *Store) CreateOrder(o *Order) error {
	o.CreatedAt = time.Now().UTC()
	if o.Status == "" {
		o.Status = OrderPending
	}
	res, err := s.db.Exec(`INSERT INTO bot_orders (telegram_id, plan, gb, price,
		list_price, status, paid_from, client_id, email, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		o.TelegramID, o.Plan, o.GB, o.Price, o.ListPrice, o.Status,
		o.PaidFrom, o.ClientID, o.Email, o.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return err
	}
	o.ID, _ = res.LastInsertId()
	return nil
}

// GetOrder loads one.
func (s *Store) GetOrder(id int64) (*Order, error) {
	var o Order
	var created string
	err := s.db.QueryRow(`SELECT id, telegram_id, plan, gb, price, list_price,
		status, paid_from, client_id, email, created_at FROM bot_orders WHERE id = ?`, id).
		Scan(&o.ID, &o.TelegramID, &o.Plan, &o.GB, &o.Price, &o.ListPrice,
			&o.Status, &o.PaidFrom, &o.ClientID, &o.Email, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &o, nil
}

// MarkOrder moves an order forward. It refuses to move one that has
// already been delivered, which is what stops a second tap on the
// operator's approve button from handing out a second account.
func (s *Store) MarkOrder(id int64, status, paidFrom string) error {
	res, err := s.db.Exec(`UPDATE bot_orders SET status = ?,
		paid_from = CASE WHEN ? = '' THEN paid_from ELSE ? END
		WHERE id = ? AND status <> ?`, status, paidFrom, paidFrom, id, OrderDelivered)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("order %d: already delivered or missing", id)
	}
	return nil
}

// AttachOrderClient records which account an order produced.
func (s *Store) AttachOrderClient(id, clientID int64, email string) error {
	_, err := s.db.Exec(`UPDATE bot_orders SET client_id = ?, email = ?,
		status = ? WHERE id = ?`, clientID, email, OrderDelivered, id)
	return err
}

// OrdersOf lists someone's purchases, newest first.
func (s *Store) OrdersOf(telegramID int64, limit int) ([]Order, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, telegram_id, plan, gb, price, list_price,
		status, paid_from, client_id, email, created_at FROM bot_orders
		WHERE telegram_id = ? ORDER BY id DESC LIMIT ?`, telegramID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		var created string
		if err := rows.Scan(&o.ID, &o.TelegramID, &o.Plan, &o.GB, &o.Price,
			&o.ListPrice, &o.Status, &o.PaidFrom, &o.ClientID, &o.Email,
			&created); err != nil {
			return nil, err
		}
		o.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, o)
	}
	return out, rows.Err()
}

// BotOffset remembers how far the bot has read its update feed, so a
// restart does not replay old messages and sell the same thing twice.
func (s *Store) BotOffset() (int64, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM bot_state WHERE key = 'offset'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int64
	fmt.Sscan(v, &n)
	return n, nil
}

// SetBotOffset stores it.
func (s *Store) SetBotOffset(n int64) error {
	_, err := s.db.Exec(`INSERT INTO bot_state (key, value) VALUES ('offset', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(n))
	return err
}
