package oauthserver

import "testing"

func TestScopeSubsetAcceptsEqualOrNarrower(t *testing.T) {
	if !ScopeIsSubset("read:zones", "read:zones write:zones") {
		t.Fatal("expected subset")
	}
	if !ScopeIsSubset("read:zones write:zones", "write:zones read:zones") {
		t.Fatal("expected subset regardless of order")
	}
	if !ScopeIsSubset("", "read:zones") {
		t.Fatal("empty requested scope is always a subset")
	}
	if !ScopeIsSubset("", "") {
		t.Fatal("empty/empty is a subset")
	}
}

func TestScopeSubsetRejectsAnythingNotGranted(t *testing.T) {
	if ScopeIsSubset("read:zones write:zones", "read:zones") {
		t.Fatal("expected not a subset")
	}
	if ScopeIsSubset("delete:zones", "read:zones write:zones") {
		t.Fatal("expected not a subset")
	}
}
