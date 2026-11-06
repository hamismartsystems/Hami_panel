package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestJoinChannels(t *testing.T) {
	cases := map[string][]string{
		"":                                            nil,
		"@a":                                          {"@a"},
		" @a , @b,,@a ":                               {"@a", "@b"},
		"@scienceandtechnology2020\n@scorpionvpn2026": {"@scienceandtechnology2020", "@scorpionvpn2026"},
	}
	for in, want := range cases {
		if got := joinChannels(in); !reflect.DeepEqual(got, want) {
			t.Errorf("joinChannels(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMemberAllWith(t *testing.T) {
	ctx := context.Background()
	chans := []string{"@a", "@b"}
	fake := func(members map[string]bool, errOn string) memberLookup {
		return func(_ context.Context, ch string, _ int64) (bool, error) {
			if ch == errOn {
				return false, errors.New("boom")
			}
			return members[ch], nil
		}
	}
	cases := []struct {
		name    string
		members map[string]bool
		errOn   string
		want    bool
		wantErr bool
	}{
		{"member of both", map[string]bool{"@a": true, "@b": true}, "", true, false},
		{"only first", map[string]bool{"@a": true}, "", false, false},
		{"only second", map[string]bool{"@b": true}, "", false, false},
		{"neither", map[string]bool{}, "", false, false},
		{"lookup error on second", map[string]bool{"@a": true, "@b": true}, "@b", false, true},
	}
	for _, c := range cases {
		got, err := memberAllWith(ctx, chans, 1, fake(c.members, c.errOn))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
