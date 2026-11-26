package support

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
)

func TestASessionEndsWithTheGrantButNeverAfterTheLongestSession(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	max := now.Add(time.Duration(identity.SupportSessionMaxHours) * time.Hour)

	short := now.Add(time.Hour)
	require.Equal(t, short, sessionExpiry(short, now), "a grant that ends soon ends the session with it")

	long := now.Add(30 * 24 * time.Hour)
	require.Equal(t, max, sessionExpiry(long, now), "a long grant does not make a long session")

	require.Equal(t, max, sessionExpiry(max, now), "exactly the longest")
}

func TestAGrantViewSaysWhetherItIsActiveAndListsItsSessions(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Minute)
	g := identity.SupportGrant{ID: uuid.New(), Reason: "outage", Role: "admin", GrantedBy: uuid.New(),
		ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour),
		Sessions: []identity.SupportSession{{MemberID: uuid.New(), StartedByEmail: "agent@vendor.io", StartedAt: started}}}

	v := view(g, now)
	require.True(t, v.Active)
	require.Empty(t, v.RevokedAt)
	require.Len(t, v.Sessions, 1)
	require.Equal(t, "agent@vendor.io", v.Sessions[0].StartedByEmail)

	require.False(t, view(g, now.Add(2*time.Hour)).Active, "lapsed")
	revoked := now
	g.RevokedAt = &revoked
	require.False(t, view(g, now).Active, "revoked")
	require.NotEmpty(t, view(g, now).RevokedAt)
}

func TestGrantErrorsAreMappedAndOthersPassThrough(t *testing.T) {
	cases := map[error]int{
		identity.ErrNotFound:             http.StatusNotFound,
		identity.ErrSupportGrantInactive: http.StatusConflict,
		identity.ErrSupportReason:        http.StatusBadRequest,
	}
	for in, want := range cases {
		se, ok := crud.AsServiceError(mapErr(in))
		require.True(t, ok, in.Error())
		require.Equal(t, want, int(se.Status), in.Error())
	}
	boom := errors.New("boom")
	require.Equal(t, boom, mapErr(boom))
}
