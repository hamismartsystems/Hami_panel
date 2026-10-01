// Package bot is the sales front end: a Telegram bot that quotes,
// takes payment and hands over a working account.
//
// It lives inside the panel rather than beside it, so a sale creates the
// customer through the same code path the dashboard uses — including
// pushing the change into the running core. A bot that writes a row the
// core never hears about sells people nothing.
package bot

import "fmt"

// Plan is what a customer can buy.
type Plan struct {
	Key   string
	Name  string
	PerGB int64 // toman per gigabyte
	Days  int   // 0 means the account never expires on time
}

// Plans are the two the operator sells. Monthly is cheaper per gigabyte
// because it also expires; the untimed one costs more for the same
// volume precisely because it does not.
var Plans = []Plan{
	{Key: "monthly", Name: "ماهانه", PerGB: 20000, Days: 30},
	{Key: "forever", Name: "زمان نامحدود", PerGB: 50000, Days: 0},
}

// PlanByKey finds one, or false.
func PlanByKey(key string) (Plan, bool) {
	for _, p := range Plans {
		if p.Key == key {
			return p, true
		}
	}
	return Plan{}, false
}

// MaxGB is a guard against a typo turning into a thousand-gigabyte order.
const MaxGB = 1000

// Quote is what a particular buyer pays.
type Quote struct {
	Plan      Plan
	GB        int
	ListPrice int64 // the ordinary customer price
	Price     int64 // what this buyer pays
	Reseller  bool
}

// Saved is the discount in toman.
func (q Quote) Saved() int64 { return q.ListPrice - q.Price }

// QuoteFor prices an order. A reseller pays half; the halving is done on
// the total rather than the per-gigabyte rate so the arithmetic matches
// what the customer is shown, to the toman.
func QuoteFor(p Plan, gb int, reseller bool) (Quote, error) {
	if gb <= 0 {
		return Quote{}, fmt.Errorf("volume must be at least one gigabyte")
	}
	if gb > MaxGB {
		return Quote{}, fmt.Errorf("volume above %d gigabytes needs to be arranged by hand", MaxGB)
	}
	list := int64(gb) * p.PerGB
	price := list
	if reseller {
		price = list / 2
	}
	return Quote{Plan: p, GB: gb, ListPrice: list, Price: price, Reseller: reseller}, nil
}

// Toman formats an amount the way the messages show it: grouped in
// threes, which is how prices are read in Persian.
func Toman(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
