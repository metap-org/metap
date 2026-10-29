package cache

import "testing"

func TestValidateKeyAcceptsANormalKey(t *testing.T) {
	if err := validateKey("policies:crm.customers"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateKeyRejectsEmptyControlCharsAndOverlong(t *testing.T) {
	if validateKey("") == nil {
		t.Fatal("expected error for empty key")
	}
	if validateKey("a\x00b") == nil {
		t.Fatal("expected error for control character")
	}
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	if validateKey(string(long)) == nil {
		t.Fatal("expected error for overlong key")
	}
}
