package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

// coreDrift reports whether the config file the core reads has stopped
// matching the panel's enabled clients. A switched-off customer must not
// stay in that file: if it does, the core keeps serving them after the
// panel has already said no. Checking the file against the database on
// every round catches a missed push no matter how it was missed, and the
// push itself is the same rebuild the panel does for any other change.
func coreDrift(st *store.Store, configPath string) (bool, string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return false, "", err
	}
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, "", fmt.Errorf("reading %s: %w", configPath, err)
	}
	have := map[string]bool{}
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "api" {
			continue
		}
		for _, c := range ib.Settings.Clients {
			have[ib.Tag+"|"+strings.ToLower(c.Email)] = true
		}
	}
	eps, err := xray.EndpointsFromStore(st)
	if err != nil {
		return false, "", err
	}
	want := map[string]bool{}
	for _, ep := range eps {
		tag := fmt.Sprintf("in-%d", ep.Inbound.ID)
		for _, c := range ep.Clients {
			want[tag+"|"+strings.ToLower(c.Email)] = true
		}
	}
	var extra, missing []string
	for k := range have {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	for k := range want {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	if len(extra)+len(missing) == 0 {
		return false, "", nil
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return true, fmt.Sprintf("still in the core file but switched off: %v; enabled but missing: %v", extra, missing), nil
}
