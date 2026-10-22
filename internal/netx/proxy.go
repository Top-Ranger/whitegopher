// Package netx holds client-address handling shared by the DAV and admin
// handlers: trusted reverse proxies and failed-login rate limiting.
package netx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies lists the reverse proxies whose X-Forwarded-* headers are believed.
type Proxies []netip.Prefix

// Contains reports whether a is one of the proxies.
func (p Proxies) Contains(a netip.Addr) bool {
	for _, pre := range p {
		if pre.Contains(a) {
			return true
		}
	}
	return false
}

func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// PeerTrusted reports whether the request arrived from a trusted proxy.
func (p Proxies) PeerTrusted(r *http.Request) bool {
	a, err := netip.ParseAddr(peerHost(r))
	return err == nil && p.Contains(a.Unmap())
}

// IsHTTPS reports whether the client reached us over TLS, directly or via a
// trusted proxy.
func (p Proxies) IsHTTPS(r *http.Request) bool {
	return r.TLS != nil || (p.PeerTrusted(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// ClientIP returns the address to attribute a request to. Behind a trusted
// proxy that is the rightmost X-Forwarded-For entry that is not itself a
// trusted proxy; otherwise the direct peer.
func (p Proxies) ClientIP(r *http.Request) string {
	host := peerHost(r)
	if !p.PeerTrusted(r) {
		return host
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return host // malformed header: fall back to the peer
		}
		if !p.Contains(a.Unmap()) {
			return a.Unmap().String()
		}
	}
	return host
}
