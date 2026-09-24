// Package clientip resolves the caller's IP address from a request.
//
// X-Forwarded-For and X-Real-IP are written by whoever is in front of the
// service — and, if nothing is in front of it, by the caller themselves. Every
// IP in this API ends up in an onboarding audit log or an auth audit log, so
// honouring those headers unconditionally means the audit trail records
// whatever address an attacker felt like typing.
//
// The rule here is the usual one: trust a forwarding header only when the
// connection itself came from a proxy we know about.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver decides which IP to attribute a request to.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver builds a resolver from CIDR strings (e.g. "10.0.0.0/8",
// "172.18.0.0/16"). A bare address ("127.0.0.1") is accepted and treated as a
// single-host prefix. Unparseable entries are skipped and reported.
func NewResolver(cidrs []string) (*Resolver, []string) {
	var (
		trusted []netip.Prefix
		bad     []string
	)
	for _, raw := range cidrs {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			trusted = append(trusted, prefix)
			continue
		}
		if addr, err := netip.ParseAddr(entry); err == nil {
			trusted = append(trusted, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		bad = append(bad, entry)
	}
	return &Resolver{trusted: trusted}, bad
}

// TrustsAny reports whether any proxy is trusted at all.
func (r *Resolver) TrustsAny() bool {
	return r != nil && len(r.trusted) > 0
}

// Resolve returns the address to attribute the request to.
//
// With no trusted proxies configured, that is always the peer address: the
// headers are ignored entirely. With trusted proxies, the right-most entry of
// X-Forwarded-For that is not itself a trusted proxy wins — walking from the
// right is what stops a client-supplied prefix from being believed.
func (r *Resolver) Resolve(req *http.Request) string {
	peer := peerAddr(req.RemoteAddr)

	if r == nil || len(r.trusted) == 0 || !r.isTrusted(peer) {
		return peer
	}

	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if candidate == "" {
				continue
			}
			if r.isTrusted(candidate) {
				continue // another hop of our own infrastructure
			}
			if _, err := netip.ParseAddr(candidate); err != nil {
				continue // junk in the header; keep looking
			}
			return candidate
		}
	}

	if xri := strings.TrimSpace(req.Header.Get("X-Real-Ip")); xri != "" {
		if _, err := netip.ParseAddr(xri); err == nil {
			return xri
		}
	}

	return peer
}

func (r *Resolver) isTrusted(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// peerAddr strips the port from a RemoteAddr, tolerating a bare address.
func peerAddr(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
