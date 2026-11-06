package traffic

import (
	"testing"
	"time"
)

// The owner decided that no customer loses any of the volume they bought:
// a client is switched off when the quota is used up, not earlier.
func TestNoEarlySwitchOff(t *testing.T) {
	st := openStore(t)
	seedClient(t, st, "full", 10_000_000)
	res, err := Apply(st, []Sample{{Email: "full", Up: 4_950_000, Down: 4_950_000}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Disabled) != 0 {
		t.Fatalf("switched off before the quota was used up: %+v", res)
	}
	res, err = Apply(st, []Sample{{Email: "full", Up: 100_000, Down: 100_000}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Disabled) != 1 {
		t.Fatalf("not switched off after the quota was used up: %+v", res)
	}
}
