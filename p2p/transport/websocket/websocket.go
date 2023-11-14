// Package websocket implements a websocket based transport for go-libp2p.
package websocket

import (
	"context"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
)

// WsFmt is multiaddr formatter for WsProtocol
var WsFmt = mafmt.And(mafmt.TCP, mafmt.Base(ma.P_WS))

var dialMatcher = mafmt.And(
	mafmt.Or(mafmt.IP, mafmt.DNS),
	mafmt.Base(ma.P_TCP),
	mafmt.Or(
		mafmt.Base(ma.P_WS),
		mafmt.And(
			mafmt.Or(
				mafmt.And(
					mafmt.Base(ma.P_TLS),
					mafmt.Base(ma.P_SNI)),
				mafmt.Base(ma.P_TLS),
			),
			mafmt.Base(ma.P_WS)),
		mafmt.Base(ma.P_WSS)))

var (
	wssComponent   = ma.StringCast("/wss")
	tlsWsComponent = ma.StringCast("/tls/ws")
	tlsComponent   = ma.StringCast("/tls")
	wsComponent    = ma.StringCast("/ws")
)

func init() {
	manet.RegisterFromNetAddr(ParseWebsocketNetAddr, "websocket")
	manet.RegisterToNetAddr(ConvertWebsocketMultiaddrToNetAddr, "ws")
	manet.RegisterToNetAddr(ConvertWebsocketMultiaddrToNetAddr, "wss")
}

type Option func(*WebsocketTransport) error

func New(u transport.Upgrader, rcmgr network.ResourceManager, opts ...Option) (*WebsocketTransport, error) {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
	t := &WebsocketTransport{
		upgrader: u,
		rcmgr:    rcmgr,
	}
	t.osNew()
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *WebsocketTransport) CanDial(a ma.Multiaddr) bool {
	return dialMatcher.Matches(a)
}

func (t *WebsocketTransport) Protocols() []int {
	return []int{ma.P_WS, ma.P_WSS}
}

func (t *WebsocketTransport) Proxy() bool {
	return false
}

func (t *WebsocketTransport) Resolve(_ context.Context, maddr ma.Multiaddr) ([]ma.Multiaddr, error) {
	parsed, err := parseWebsocketMultiaddr(maddr)
	if err != nil {
		return nil, err
	}

	if !parsed.isWSS {
		// No /tls/ws component, this isn't a secure websocket multiaddr. We can just return it here
		return []ma.Multiaddr{maddr}, nil
	}

	if parsed.sni == nil {
		var err error
		// We don't have an sni component, we'll use dns/dnsaddr
		ma.ForEach(parsed.restMultiaddr, func(c ma.Component) bool {
			switch c.Protocol().Code {
			case ma.P_DNS, ma.P_DNS4, ma.P_DNS6:
				// err shouldn't happen since this means we couldn't parse a dns hostname for an sni value.
				parsed.sni, err = ma.NewComponent("sni", c.Value())
				return false
			}
			return true
		})
		if err != nil {
			return nil, err
		}
	}

	if parsed.sni == nil {
		// we didn't find anything to set the sni with. So we just return the given multiaddr
		return []ma.Multiaddr{maddr}, nil
	}

	return []ma.Multiaddr{parsed.toMultiaddr()}, nil
}

func (t *WebsocketTransport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	connScope, err := t.rcmgr.OpenConnection(network.DirOutbound, true, raddr)
	if err != nil {
		return nil, err
	}
	c, err := t.dialWithScope(ctx, raddr, p, connScope)
	if err != nil {
		connScope.Done()
		return nil, err
	}
	return c, nil
}

func (t *WebsocketTransport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, connScope network.ConnManagementScope) (transport.CapableConn, error) {
	macon, err := t.maDial(ctx, raddr)
	if err != nil {
		return nil, err
	}
	conn, err := t.upgrader.Upgrade(ctx, t, macon, network.DirOutbound, p, connScope)
	if err != nil {
		return nil, err
	}
	return &capableConn{CapableConn: conn}, nil
}
