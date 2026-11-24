package apikeys

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
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

type spyAudit struct {
	mu      sync.Mutex
	entries []spyEntry
}

type spyEntry struct {
	entity string
	id     uuid.UUID
	action audit.AuditAction
	diff   map[string][2]any
}

func (s *spyAudit) Record(_ context.Context, _ kernel.Caller, entity string, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, spyEntry{entity, id, action, diff})
}

type world struct {
	svc    *Service
	store  *memStore
	audit  *spyAudit
	tenant uuid.UUID
	now    time.Time
	cap    int
}

func newWorld() *world {
	w := &world{store: newMemStore(), audit: &spyAudit{}, tenant: uuid.New(), now: time.Now().UTC()}
	w.svc = New(w.store, w.audit, func(context.Context, uuid.UUID) int { return w.cap }, func() time.Time { return w.now })
	return w
}

// as is a request by member holding roles.
func (w *world) as(member uuid.UUID, roles ...string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: w.tenant.String(), UserID: member.String(), Roles: roles, HasRoles: true})
}

func statusOf(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

func (w *world) admin() (uuid.UUID, context.Context) {
	id := w.store.member(w.tenant)
	return id, w.as(id, "admin")
}

func TestAnAdminIssuesAKeyAndTheSecretIsInTheAnswerAndTheAuditNeverHoldsIt(t *testing.T) {
	w := newWorld()
	_, asAdmin := w.admin()
	bot := w.store.member(w.tenant)

	got, err := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "pipeline", ExpiresInDays: 30})
	require.NoError(t, err)
	require.NotEmpty(t, got.Token)
	require.Contains(t, got.Token, got.ApiKey.Prefix)
	require.Equal(t, bot.String(), got.ApiKey.UserId)
	require.NotEmpty(t, got.ApiKey.ExpiresAt)

	require.Len(t, w.audit.entries, 1)
	e := w.audit.entries[0]
	require.Equal(t, AuditEntityUsers, e.entity)
	require.Equal(t, bot, e.id, "recorded under the member the key acts as")
	require.Equal(t, "pipeline ("+got.ApiKey.Prefix+")", e.diff["apiKeyCreated"][1])
	for _, v := range e.diff {
		require.NotContains(t, v[1], got.Token, "the secret is never audited")
	}
}

func TestOnlyAnAdminManagesKeysAndASupportSessionMayNotIssueOne(t *testing.T) {
	w := newWorld()
	bot := w.store.member(w.tenant)
	plain := w.as(w.store.member(w.tenant)) // a member with no role
	req := &CreateApiKeyRequest{UserId: bot.String(), Name: "k"}

	_, err := w.svc.Create(plain, req)
	require.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = w.svc.List(plain, bot.String())
	require.Equal(t, http.StatusForbidden, statusOf(err))
	require.Equal(t, http.StatusForbidden, statusOf(w.svc.Revoke(plain, uuid.NewString())))

	support := w.store.supportMember(w.tenant)
	asSupport := w.as(support, "admin")
	_, err = w.svc.Create(asSupport, req)
	require.Equal(t, http.StatusForbidden, statusOf(err), "a vendor's support session cannot create access that outlives the grant")
	_, err = w.svc.List(asSupport, bot.String())
	require.NoError(t, err, "but it may look")
	_, err = w.svc.Create(context.Background(), req)
	require.Equal(t, http.StatusUnauthorized, statusOf(err))
	require.Empty(t, w.audit.entries)
}

func TestWhatIsAskedIsCheckedBeforeAnythingIsIssued(t *testing.T) {
	w := newWorld()
	_, asAdmin := w.admin()
	bot := w.store.member(w.tenant)

	_, err := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: "nope", Name: "k"})
	require.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "k", ExpiresInDays: 5000})
	require.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "  "})
	require.Equal(t, http.StatusBadRequest, statusOf(err))
	_, err = w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: uuid.NewString(), Name: "k"})
	require.Equal(t, http.StatusNotFound, statusOf(err), "no such member")

	list, _ := w.svc.List(asAdmin, bot.String())
	require.Empty(t, list.ApiKeys, "nothing was issued")
	require.Empty(t, w.audit.entries)
}

func TestTheTenantsCapOnLifetimeApplies(t *testing.T) {
	w := newWorld()
	_, asAdmin := w.admin()
	bot := w.store.member(w.tenant)
	forever := &CreateApiKeyRequest{UserId: bot.String(), Name: "forever"}

	_, err := w.svc.Create(asAdmin, forever)
	require.NoError(t, err, "no cap: a key that never expires is fine")

	w.cap = 90
	for _, days := range []int32{0, 91, 3650} {
		_, err = w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "long", ExpiresInDays: days})
		require.Equal(t, http.StatusBadRequest, statusOf(err), "expiresInDays=%d", days)
	}
	got, err := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "ok", ExpiresInDays: 90})
	require.NoError(t, err)
	require.NotEmpty(t, got.ApiKey.ExpiresAt)
}

func TestARevokedKeyShowsAsRevokedAndRevokingTwiceIsFine(t *testing.T) {
	w := newWorld()
	_, asAdmin := w.admin()
	bot := w.store.member(w.tenant)
	made, err := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "k"})
	require.NoError(t, err)

	require.NoError(t, w.svc.Revoke(asAdmin, made.ApiKey.Id))
	require.NoError(t, w.svc.Revoke(asAdmin, made.ApiKey.Id))
	list, err := w.svc.List(asAdmin, bot.String())
	require.NoError(t, err)
	require.Len(t, list.ApiKeys, 1)
	require.NotEmpty(t, list.ApiKeys[0].RevokedAt)

	require.Len(t, w.audit.entries, 3, "the issue and each revoke")
	require.Equal(t, made.ApiKey.Id, w.audit.entries[1].diff["apiKeyRevoked"][1])
}

func TestAnotherTenantsKeyIsNotFoundAndNotRevoked(t *testing.T) {
	w := newWorld()
	_, asAdmin := w.admin()
	bot := w.store.member(w.tenant)
	made, _ := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "k"})

	other := newWorld()
	other.store = w.store // the same keys, a different tenant asking
	other.svc = New(w.store, other.audit, nil, nil)
	_, asOther := other.admin()
	require.Equal(t, http.StatusNotFound, statusOf(other.svc.Revoke(asOther, made.ApiKey.Id)))
	require.Equal(t, http.StatusBadRequest, statusOf(other.svc.Revoke(asOther, "not-a-uuid")))

	list, _ := w.svc.List(asAdmin, bot.String())
	require.Empty(t, list.ApiKeys[0].RevokedAt)
}
