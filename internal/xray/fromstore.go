package xray

import (
	"fmt"

	"github.com/hamismartsystems/hami_panel/internal/link"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// EndpointsFromStore loads enabled inbounds and their enabled clients.
// A disabled inbound is omitted entirely, so its secrets cannot leak into
// the config of an inbound that is actually being served.
func EndpointsFromStore(st *store.Store) ([]Endpoint, error) {
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
			if !c.Enable {
				continue
			}
			enabled = append(enabled, link.Client{
				UUID: c.UUID, Password: c.Password, Email: c.Email,
				Method: c.Method, SSPassword: c.SSPassword,
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
