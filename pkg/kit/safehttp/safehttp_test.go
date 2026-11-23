package safehttp

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPublicAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "203.0.114.1": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false,
		"192.0.2.1": false, "198.51.100.7": false, "203.0.113.9": false, "198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
		// An IPv6 form is as public as the IPv4 address inside it.
		"64:ff9b::808:808": true, "64:ff9b::a00:1": false, "64:ff9b::7f00:1": false, "64:ff9b::a9fe:a9fe": false,
		"2002:0808:0808::1": true, "2002:0a00:0001::1": false, "2001:0:4136:e378:8000:63bf:3fff:fdd2": false, "2001:db8::1": false, "224.0.0.1": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:10.0.0.1": false,
	} {
		if got := PublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %v want %v", addr, got, want)
		}
	}
}

func TestClientRefusesPrivateUnlessAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	defer srv.Close()
	url := srv.URL

	if _, err := NewClient(Options{Timeout: time.Second}).Get(url); err == nil || !strings.Contains(err.Error(), "not a public one") {
		t.Fatalf("a loopback server must be refused, got %v", err)
	}
	resp, err := NewClient(Options{Timeout: time.Second, AllowPrivate: true}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
