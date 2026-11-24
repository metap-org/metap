package sessions

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

// spyAudit remembers what was recorded.
type spyAudit struct {
	mu      sync.Mutex
	entries []spyEntry
}

type spyEntry struct {
	caller kernel.Caller
	entity string
	id     uuid.UUID
	action audit.AuditAction
	diff   map[string][2]any
}

func (s *spyAudit) Record(_ context.Context, c kernel.Caller, entity string, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, spyEntry{c, entity, id, action, diff})
}

type world struct {
	svc    *Service
	store  *memStore
	audit  *spyAudit
	tenant uuid.UUID
	now    time.Time
}

func newWorld() *world {
	w := &world{store: newMemStore(), audit: &spyAudit{}, tenant: uuid.New(), now: time.Now().UTC()}
	w.svc = New(w.store, w.audit, func() time.Time { return w.now })
	return w
}

// as is a request by member, using the session sid ("" for a caller that is not a session).
func (w *world) as(member uuid.UUID, sid string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: member.String(), SessionID: sid})
}

func statusOf(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

func TestViewMarksExactlyTheCallingSessionAndUsesUTC(t *testing.T) {
	mine, other := uuid.New(), uuid.New()
	local := time.FixedZone("UTC+7", 7*3600)
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, local)
	out := view([]identity.Session{
		{ID: mine, TenantID: uuid.New(), CreatedAt: at, LastSeenAt: at, ExpiresAt: at},
		{ID: other, TenantID: uuid.New(), CreatedAt: at, LastSeenAt: at, ExpiresAt: at},
	}, mine.String())
	require.True(t, out[0].Current)
	require.False(t, out[1].Current)
	require.Equal(t, time.UTC, out[0].CreatedAt.Location())
	require.Equal(t, 2, out[0].CreatedAt.Hour(), "09:00 at UTC+7 is 02:00 UTC")
	require.Empty(t, view(nil, ""), "no sessions is an empty list, not nil")
	require.NotNil(t, view(nil, ""))
}

func TestKeepSessionIsNilForACallerThatIsNotASession(t *testing.T) {
	require.Equal(t, uuid.Nil, keepSession(""))
	require.Equal(t, uuid.Nil, keepSession("not-a-uuid"))
	id := uuid.New()
	require.Equal(t, id, keepSession(id.String()))
}

func TestListShowsOnlyTheCallersSessionsAndMarksTheCurrentOne(t *testing.T) {
	w := newWorld()
	bob, eve := w.store.person(w.tenant), w.store.person(w.tenant)
	laptop := w.store.signIn(w.tenant, bob, time.Hour, w.now)
	w.store.signIn(w.tenant, bob, time.Hour, w.now)
	w.store.signIn(w.tenant, eve, time.Hour, w.now)

	got, err := w.svc.List(w.as(bob, laptop.String()))
	require.NoError(t, err)
	require.Len(t, got, 2)
	current := 0
	for _, s := range got {
		if s.Current {
			current++
			require.Equal(t, laptop.String(), s.ID)
		}
	}
	require.Equal(t, 1, current)
}

func TestARevokedSessionIsGoneAndTheRevocationIsAudited(t *testing.T) {
	w := newWorld()
	bob := w.store.person(w.tenant)
	laptop := w.store.signIn(w.tenant, bob, time.Hour, w.now)
	phone := w.store.signIn(w.tenant, bob, time.Hour, w.now)

	require.NoError(t, w.svc.Revoke(w.as(bob, laptop.String()), phone.String()))
	got, _ := w.svc.List(w.as(bob, laptop.String()))
	require.Len(t, got, 1)

	require.Len(t, w.audit.entries, 1)
	e := w.audit.entries[0]
	require.Equal(t, AuditEntityUsers, e.entity)
	require.Equal(t, bob, e.id, "recorded under the caller's own member")
	require.Equal(t, bob, e.caller.Self)
	require.Equal(t, audit.ActionUpdate, e.action)
	require.Equal(t, [2]any{phone.String(), "revoked"}, e.diff["session"])
}

func TestRevokingWhatIsNotYoursOrNotAnIdIsNotFoundAndNotAudited(t *testing.T) {
	w := newWorld()
	bob, eve := w.store.person(w.tenant), w.store.person(w.tenant)
	phone := w.store.signIn(w.tenant, bob, time.Hour, w.now)

	require.Equal(t, http.StatusNotFound, statusOf(w.svc.Revoke(w.as(eve, ""), phone.String())), "Eve cannot end Bob's session")
	require.Equal(t, http.StatusNotFound, statusOf(w.svc.Revoke(w.as(bob, ""), "not-a-uuid")))
	require.NoError(t, w.svc.Revoke(w.as(bob, ""), phone.String()))
	require.Equal(t, http.StatusNotFound, statusOf(w.svc.Revoke(w.as(bob, ""), phone.String())), "already ended")
	require.Len(t, w.audit.entries, 1, "only the revocation that happened is audited")
}

func TestSigningOutEverywhereElseKeepsTheCallingSession(t *testing.T) {
	w := newWorld()
	bob, eve := w.store.person(w.tenant), w.store.person(w.tenant)
	laptop := w.store.signIn(w.tenant, bob, time.Hour, w.now)
	w.store.signIn(w.tenant, bob, time.Hour, w.now)
	w.store.signIn(w.tenant, eve, time.Hour, w.now)

	n, err := w.svc.RevokeOthers(w.as(bob, laptop.String()))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	mine, _ := w.svc.List(w.as(bob, laptop.String()))
	require.Len(t, mine, 1)
	require.Equal(t, [2]any{1, "revoked"}, w.audit.entries[0].diff["sessions"])
	eves, _ := w.svc.List(w.as(eve, ""))
	require.Len(t, eves, 1, "another person's session is untouched")
}

func TestACallerWithoutAnAccountOrWithoutCredentialsIsRefused(t *testing.T) {
	w := newWorld()
	_, err := w.svc.List(context.Background())
	require.Equal(t, http.StatusUnauthorized, statusOf(err))
	_, err = w.svc.List(w.as(uuid.New(), "")) // signed in, but no membership behind it
	require.Equal(t, http.StatusNotFound, statusOf(err))
	require.Equal(t, http.StatusNotFound, statusOf(w.svc.Revoke(w.as(uuid.New(), ""), uuid.NewString())))
}

func TestWithoutAnAuditorNothingIsRecordedAndNothingFails(t *testing.T) {
	w := newWorld()
	svc := New(w.store, nil, nil)
	bob := w.store.person(w.tenant)
	phone := w.store.signIn(w.tenant, bob, time.Hour, time.Now())
	require.NoError(t, svc.Revoke(w.as(bob, ""), phone.String()))
}
