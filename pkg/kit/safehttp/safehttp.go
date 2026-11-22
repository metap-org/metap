// Package safehttp is an HTTP client for addresses a customer chose: an origin to probe, a file to
// fetch to prove ownership. The address is theirs to choose, so it is also theirs to point at our
// own network; this client resolves the name itself and refuses to connect to anything that is not
// a public address (SSRF guard), unless private targets are allowed (dev, where origins and test
// hostnames are on localhost or a mock). It follows no redirect. Standard library only.
package safehttp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// ErrPrivateTarget is what a refused connection reports.
var ErrPrivateTarget = errors.New("this address is not a public one, so it is not contacted")

// Resolver returns the addresses of host. A literal IP is handled before the resolver is asked.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// Options configure NewClient.
type Options struct {
	Timeout time.Duration
	// ServerName is presented in TLS when set (the Host the origin expects).
	ServerName string
	// AllowPrivate lets the client reach private addresses. Dev only.
	AllowPrivate bool
	// Resolve looks a name up; nil means the system resolver.
	Resolve Resolver
}

// PublicAddress reports whether ip is one a customer's site could legitimately live on.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	// Carrier-grade NAT (100.64.0.0/10), the "this network" block and the reserved space that
	// netip does not call private.
	for _, p := range reserved {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

var reserved = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{"100.64.0.0/10", "0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", "fc00::/7", "fe80::/10"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// NewClient returns a client that connects only to public addresses (or any, when AllowPrivate) and
// follows no redirect.
func NewClient(o Options) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	resolve := o.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var ips []netip.Addr
			if ip, perr := netip.ParseAddr(host); perr == nil {
				ips = []netip.Addr{ip}
			} else if ips, err = resolve(ctx, host); err != nil {
				return nil, err
			}
			last := ErrPrivateTarget
			for _, ip := range ips {
				if !o.AllowPrivate && !PublicAddress(ip) {
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
		TLSClientConfig:       &tls.Config{ServerName: o.ServerName, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: o.Timeout,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Timeout:       o.Timeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
