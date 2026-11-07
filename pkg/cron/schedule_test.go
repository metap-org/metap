package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEveryFiveMinutesUTC(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	next, err := NextRunAt("0 */5 * * * *", "UTC", after)
	require.NoError(t, err)
	want := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	require.True(t, next.Equal(want), "got %v, want %v", next, want)
}

func TestDaily9amRespectsTimezoneOffset(t *testing.T) {
	// 9am in Asia/Ho_Chi_Minh (UTC+7) is 2am UTC.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := NextRunAt("0 0 9 * * *", "Asia/Ho_Chi_Minh", after)
	require.NoError(t, err)
	want := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	require.True(t, next.Equal(want), "got %v, want %v", next, want)
}

func TestRejectsBadExpression(t *testing.T) {
	require.Error(t, ValidateSchedule("not a cron expr", "UTC"), "expected an error")
}

func TestRejectsBadTimezone(t *testing.T) {
	require.Error(t, ValidateSchedule("0 */5 * * * *", "Not/A_Zone"), "expected an error")
}
