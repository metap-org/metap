package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseTrustedProxies reads proxy addresses given as CIDRs or single IPs ("10.0.0.0/8",
// "172.18.0.5"). An empty list is valid: no proxy is trusted.
func ParseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is neither a CIDR nor an IP address", e)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func trustedAddr(a netip.Addr, trusted []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP is the address of whoever is really calling. It is the TCP peer, except when that peer
// is one of the trusted proxies: then X-Forwarded-For is read from the right, skipping every
// trusted proxy, and the first address left is the client. X-Forwarded-For is never believed from
// a peer that is not trusted — anyone can send that header — so with no trusted proxies this is
// exactly the peer address.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || len(trusted) == 0 || !trustedAddr(peer, trusted) {
		return host
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return host // a header we cannot read is not evidence of anything
		}
		if !trustedAddr(a, trusted) {
			return a.Unmap().String()
		}
	}
	return host
}
