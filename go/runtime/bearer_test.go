package runtime

import "testing"

func TestParseBearerExtractsToken(t *testing.T) {
	token, ok := ParseBearer("Bearer abc123")
	if !ok || token != "abc123" {
		t.Fatalf("got (%q, %v)", token, ok)
	}
}

func TestParseBearerRejectsMissingPrefix(t *testing.T) {
	if _, ok := ParseBearer("Basic abc123"); ok {
		t.Fatal("expected not ok")
	}
}

func TestParseBearerRejectsEmptyToken(t *testing.T) {
	if _, ok := ParseBearer("Bearer "); ok {
		t.Fatal("expected not ok for \"Bearer \"")
	}
	if _, ok := ParseBearer("Bearer"); ok {
		t.Fatal("expected not ok for \"Bearer\"")
	}
}

func TestParseBearerTrimsIncidentalWhitespace(t *testing.T) {
	token, ok := ParseBearer("Bearer  abc123  ")
	if !ok || token != "abc123" {
		t.Fatalf("got (%q, %v)", token, ok)
	}
}
