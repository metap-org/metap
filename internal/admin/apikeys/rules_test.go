package apikeys

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExpiryFollowsTheAskAndTheTenantsCap(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		days, cap int
		never     bool
		problem   bool
	}{
		{"no cap, never expires", 0, 0, true, false},
		{"no cap, thirty days", 30, 0, false, false},
		{"the longest allowed", MaxLifetimeDays, 0, false, false},
		{"one past the longest", MaxLifetimeDays + 1, 0, false, true},
		{"negative", -1, 0, false, true},
		{"a cap forbids never", 0, 90, false, true},
		{"a cap forbids more", 91, 90, false, true},
		{"exactly the cap", 90, 90, false, false},
		{"under the cap", 1, 90, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at, why := expiry(c.days, c.cap, now)
			if c.problem {
				require.NotEmpty(t, why)
				require.Nil(t, at)
				return
			}
			require.Empty(t, why)
			if c.never {
				require.Nil(t, at)
				return
			}
			require.Equal(t, now.Add(time.Duration(c.days)*24*time.Hour), *at)
		})
	}
}

func TestTheCapRefusalSaysWhatToAsk(t *testing.T) {
	_, why := expiry(0, 90, time.Now())
	require.Equal(t, "This tenant limits API keys to 90 days; set expiresInDays between 1 and 90.", why)
	_, why = expiry(4000, 0, time.Now())
	require.Equal(t, "expiresInDays must be between 0 and 3650", why)
}

func TestNameProblem(t *testing.T) {
	require.Empty(t, nameProblem("pipeline"))
	require.Empty(t, nameProblem(strings.Repeat("a", 200)))
	require.NotEmpty(t, nameProblem(""))
	require.NotEmpty(t, nameProblem("   "))
	require.NotEmpty(t, nameProblem(strings.Repeat("a", 201)))
}

func TestFormatTime(t *testing.T) {
	require.Empty(t, formatTime(nil))
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 7*3600))
	require.Equal(t, "2026-01-01T20:04:05.000000006Z", formatTime(&at))
}
