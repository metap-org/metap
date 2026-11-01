package control

// Ported from the Rust metap-control crate's src/hostname.rs test module.

import "testing"

func TestNormalizesCasePortAndTrailingDot(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"App.Example.COM", "app.example.com"},
		{"app.example.com:8443", "app.example.com"},
		{"app.example.com.", "app.example.com"},
	}
	for _, c := range cases {
		got, ok := NormalizeHostname(c.raw)
		if !ok || got != c.want {
			t.Fatalf("%q: got %q ok=%v, want %q", c.raw, got, ok, c.want)
		}
	}
}

// The Host header is whatever the client typed. None of these may reach a query.
func TestRejectsAnythingThatIsNotAHostname(t *testing.T) {
	for _, raw := range []string{
		"",
		" ",
		"[::1]:8080",
		"app.example.com/../admin",
		"app.example.com' OR '1'='1",
		"app_example.com",
		"app.example.com ",
	} {
		got, ok := NormalizeHostname(raw)
		if ok && got != "app.example.com" {
			t.Fatalf("%q normalized to %q", raw, got)
		}
	}

	if _, ok := NormalizeHostname("app.example.com' OR '1'='1"); ok {
		t.Fatal("expected rejection")
	}
	if _, ok := NormalizeHostname("app_example.com"); ok {
		t.Fatal("expected rejection")
	}
	longHost := ""
	for i := 0; i < 254; i++ {
		longHost += "a"
	}
	if _, ok := NormalizeHostname(longHost); ok {
		t.Fatal("expected rejection of an over-length hostname")
	}
}
