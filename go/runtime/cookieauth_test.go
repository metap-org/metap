package runtime

import "testing"

func TestOnlySafeMethodsSkipTheCsrfCheck(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		if RequiresCSRFCheck(m) {
			t.Fatalf("%s must be exempt", m)
		}
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if !RequiresCSRFCheck(m) {
			t.Fatalf("%s must be CSRF-checked", m)
		}
	}
}

func TestCsrfMatchesOnlyWhenBothArePresentAndEqual(t *testing.T) {
	if !CSRFMatches("abc", "abc", true, true) {
		t.Fatal("expected match")
	}
	if CSRFMatches("abc", "different", true, true) {
		t.Fatal("expected no match")
	}
	if CSRFMatches("abc", "", true, false) {
		t.Fatal("header missing must not pass")
	}
	if CSRFMatches("", "abc", false, true) {
		t.Fatal("cookie missing must not pass")
	}
	if CSRFMatches("", "", false, false) {
		t.Fatal("both missing must not pass")
	}
}

func TestEmptyValuesNeverMatchEachOther(t *testing.T) {
	if CSRFMatches("", "", true, true) {
		t.Fatal("both present but empty must not match")
	}
	if CSRFMatches("", "abc", true, false) {
		t.Fatal("expected no match")
	}
}
