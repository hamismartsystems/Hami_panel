package xray

import (
	"fmt"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/link"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// EndpointsFromStore loads enabled inbounds and their enabled clients.
// A disabled inbound is omitted entirely, so its secrets cannot leak into
// the config of an inbound that is actually being served.
//
// A client that is out of term or out of quota is omitted too. The
// subscription already refuses to hand out its link, but a customer who
// saved the config earlier would keep connecting: the core has no idea
// what an expiry date is, so the only way to stop them is to leave them
// out of the config. Dropping them here is what makes the term real.
//
// The decision comes from store.Client.ActiveNow — the same rule the
// subscription and the traffic collector use — so "active" cannot mean
// one thing on the sub page and another in the running core.
func EndpointsFromStore(st *store.Store) ([]Endpoint, error) {
	return endpointsAt(st, time.Now())
}

// endpointsAt is EndpointsFromStore with the clock passed in, so tests
// can place a client before or after its expiry.
func endpointsAt(st *store.Store, now time.Time) ([]Endpoint, error) {
	inbounds, err := st.ListInbounds()
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(inbounds))
	for _, in := range inbounds {
		if !in.Enable {
			continue
		}
		clients, err := st.ListClientsOf(in.ID)
		if err != nil {
			return nil, err
		}
		enabled := make([]link.Client, 0, len(clients))
		for _, c := range clients {
			if !c.ActiveNow(now) {
				continue
			}
			enabled = append(enabled, link.Client{
				UUID: c.UUID, Password: c.Password, Email: c.Email,
				Method: c.Method, SSPassword: c.SSPassword, Flow: c.Flow,
			})
		}
		sec, err := st.GetInboundSecret(in.ID)
		if err != nil {
			return nil, fmt.Errorf("inbound %d: %w", in.ID, err)
		}
		out = append(out, Endpoint{
			Inbound: link.Inbound{
				ID: in.ID, Remark: in.Remark, Protocol: in.Protocol, Port: in.Port, Host: in.Host,
				Transport: link.Transport(in.Transport), Security: link.Security(in.Security),
				SNI: in.SNI, PublicKey: in.PublicKey, ShortID: in.ShortID, SpiderX: in.SpiderX,
				Fingerprint: in.Fingerprint, Path: in.Path, XHTTPMode: in.XHTTPMode,
				HeaderType: in.HeaderType, Flow: in.Flow,
			},
			Clients:    enabled,
			Listen:     sec.Listen,
			PrivateKey: sec.PrivateKey,
			Dest:       sec.Dest,
			CertFile:   sec.CertFile,
			KeyFile:    sec.KeyFile,
			Tag:        fmt.Sprintf("in-%d", in.ID),
		})
	}
	return out, nil
}
