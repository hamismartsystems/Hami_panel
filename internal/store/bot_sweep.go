package store

import "time"

// DeliveredBotOrders lists every sales-bot order that produced an
// account, oldest first. The sweep uses it to find candidates.
func (s *Store) DeliveredBotOrders() ([]Order, error) {
	rows, err := s.db.Query(`SELECT id, telegram_id, plan, gb, price, list_price,
		status, paid_from, client_id, email, created_at, quantity
		FROM bot_orders WHERE status = ? AND client_id > 0 ORDER BY id`, OrderDelivered)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		var created string
		if err := rows.Scan(&o.ID, &o.TelegramID, &o.Plan, &o.GB, &o.Price, &o.ListPrice,
			&o.Status, &o.PaidFrom, &o.ClientID, &o.Email, &created, &o.Quantity); err != nil {
			return nil, err
		}
		o.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, o)
	}
	return out, rows.Err()
}
