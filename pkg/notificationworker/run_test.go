package notificationworker

// The Rust metap-notification-worker crate has no unit tests of its own (confirmed by reading
// its source) — this covers notify's pure parsing logic, added during the port.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJsonStringOrReturnsTheValueWhenPresent(t *testing.T) {
	got := jsonStringOr([]byte(`"approve"`), "?")
	require.Equal(t, "approve", got, "got %q", got)
}

func TestJsonStringOrFallsBackWhenAbsentOrWrongType(t *testing.T) {
	got := jsonStringOr(nil, "?")
	require.Equal(t, "?", got, "got %q", got)
	got = jsonStringOr([]byte(`42`), "?")
	require.Equal(t, "?", got, "got %q", got)
}
