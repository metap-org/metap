package notificationworker

// The Rust metap-notification-worker crate has no unit tests of its own (confirmed by reading
// its source) — this covers notify's pure parsing logic, added during the port.

import "testing"

func TestJsonStringOrReturnsTheValueWhenPresent(t *testing.T) {
	if got := jsonStringOr([]byte(`"approve"`), "?"); got != "approve" {
		t.Fatalf("got %q", got)
	}
}

func TestJsonStringOrFallsBackWhenAbsentOrWrongType(t *testing.T) {
	if got := jsonStringOr(nil, "?"); got != "?" {
		t.Fatalf("got %q", got)
	}
	if got := jsonStringOr([]byte(`42`), "?"); got != "?" {
		t.Fatalf("got %q", got)
	}
}
