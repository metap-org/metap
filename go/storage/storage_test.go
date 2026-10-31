package storage

import "testing"

func TestValidateKeyAcceptsANormalKey(t *testing.T) {
	if err := validateKey("avatars/user-123.png"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateKeyRejectsTraversalSegments(t *testing.T) {
	for _, bad := range []string{"../other-tenant/secret", "a/../b", "./a"} {
		if validateKey(bad) == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestValidateKeyRejectsLeadingSlashEmptyAndControlChars(t *testing.T) {
	if validateKey("/absolute") == nil {
		t.Fatal("expected rejection for leading slash")
	}
	if validateKey("") == nil {
		t.Fatal("expected rejection for empty key")
	}
	if validateKey("a\x00b") == nil {
		t.Fatal("expected rejection for control character")
	}
	long := make([]byte, 1025)
	for i := range long {
		long[i] = 'a'
	}
	if validateKey(string(long)) == nil {
		t.Fatal("expected rejection for overlong key")
	}
}
