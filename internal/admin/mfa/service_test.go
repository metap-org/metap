package mfa

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
}

func newWorld() *world {
	w := &world{store: newMemStore(), audit: &spyAudit{}, tenant: uuid.New()}
	w.svc = New(w.store, w.audit, func() time.Time { return time.Now() })
	return w
}

func (w *world) as(member uuid.UUID) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{TenantID: w.tenant.String(), UserID: member.String()})
}

func status(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

func code(err error) string {
	if se, ok := crud.AsServiceError(err); ok {
		return se.Code
	}
	return ""
}

// turnOn runs the two-step enrolment through the service.
func (w *world) turnOn(t *testing.T, ctx context.Context) []string {
	_, err := w.svc.Begin(ctx)
	require.NoError(t, err)
	got, err := w.svc.Confirm(ctx, &MfaCodeRequest{Code: goodCode})
	require.NoError(t, err)
	return got.Codes
}

func TestEnrolmentTurnsItOnAndIsAuditedOnceItIsConfirmed(t *testing.T) {
	w := newWorld()
	bob := w.store.person(w.tenant)
	ctx := w.as(bob)

	off, err := w.svc.Status(ctx)
	require.NoError(t, err)
	require.False(t, off.Enabled)

	began, err := w.svc.Begin(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, began.Secret)
	require.Empty(t, w.audit.entries, "starting changes nothing yet")

	codes, err := w.svc.Confirm(ctx, &MfaCodeRequest{Code: goodCode})
	require.NoError(t, err)
	require.Len(t, codes.Codes, 10)
	on, _ := w.svc.Status(ctx)
	require.True(t, on.Enabled)
	require.EqualValues(t, 10, on.RecoveryCodesLeft)

	require.Len(t, w.audit.entries, 1)
	e := w.audit.entries[0]
	require.Equal(t, AuditEntityUsers, e.entity)
	require.Equal(t, bob, e.id)
	require.Equal(t, audit.ActionUpdate, e.action)
	require.Equal(t, [2]any{"off", "on"}, e.diff["mfa"])
}

func TestAWrongFirstCodeLeavesItOffAndUnaudited(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.store.person(w.tenant))
	_, err := w.svc.Confirm(ctx, &MfaCodeRequest{Code: goodCode})
	require.Equal(t, http.StatusConflict, status(err), "confirm before begin")
	require.Equal(t, "mfa_not_enrolled", code(err))
	_, err = w.svc.Begin(ctx)
	require.NoError(t, err)
	_, err = w.svc.Confirm(ctx, &MfaCodeRequest{Code: "000000"})
	require.Equal(t, http.StatusUnauthorized, status(err))
	require.Equal(t, "mfa_invalid_code", code(err))
	off, _ := w.svc.Status(ctx)
	require.False(t, off.Enabled)
	require.Empty(t, w.audit.entries)
}

func TestItCannotBeBegunAgainWhileOn(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.store.person(w.tenant))
	w.turnOn(t, ctx)
	_, err := w.svc.Begin(ctx)
	require.Equal(t, http.StatusConflict, status(err))
	require.Equal(t, "mfa_already_on", code(err))
}

func TestDisablingNeedsACodeAndIsAudited(t *testing.T) {
	w := newWorld()
	bob := w.store.person(w.tenant)
	ctx := w.as(bob)
	w.turnOn(t, ctx)
	w.audit.entries = nil

	require.Equal(t, http.StatusUnauthorized, status(w.svc.Disable(ctx, &MfaCodeRequest{Code: "wrong"})))
	on, _ := w.svc.Status(ctx)
	require.True(t, on.Enabled)
	require.Empty(t, w.audit.entries, "a refused disable is not a change")

	require.NoError(t, w.svc.Disable(ctx, &MfaCodeRequest{Code: goodCode}))
	off, _ := w.svc.Status(ctx)
	require.False(t, off.Enabled)
	require.Len(t, w.audit.entries, 1)
	require.Equal(t, [2]any{"on", "off"}, w.audit.entries[0].diff["mfa"])
}

func TestRegeneratingRecoveryCodesIsAuditedWithoutTheCodes(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.store.person(w.tenant))
	w.turnOn(t, ctx)
	w.audit.entries = nil

	_, err := w.svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: "wrong"})
	require.Equal(t, http.StatusUnauthorized, status(err))
	require.Empty(t, w.audit.entries, "a refused regenerate is not a change")

	got, err := w.svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: goodCode})
	require.NoError(t, err)
	require.NotEmpty(t, got.Codes)
	require.Len(t, w.audit.entries, 1)
	e := w.audit.entries[0]
	require.Equal(t, AuditEntityUsers, e.entity)
	require.Equal(t, audit.ActionUpdate, e.action)
	require.Contains(t, e.diff, "recoveryCodes")
	for _, pair := range e.diff {
		for _, v := range pair {
			for _, c := range got.Codes {
				require.NotEqual(t, c, v, "no recovery code reaches the audit trail")
			}
		}
	}
}

func TestTooManyWrongCodesLockItWithTooManyRequests(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.store.person(w.tenant))
	w.turnOn(t, ctx)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusUnauthorized, status(w.svc.Disable(ctx, &MfaCodeRequest{Code: "wrong"})))
	}
	err := w.svc.Disable(ctx, &MfaCodeRequest{Code: goodCode})
	require.Equal(t, http.StatusTooManyRequests, status(err), "even the right code is refused while locked")
	require.Equal(t, "account_locked", code(err))
}

func TestRegeneratingGivesANewSet(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.store.person(w.tenant))
	first := w.turnOn(t, ctx)
	w.audit.entries = nil

	_, err := w.svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: "wrong"})
	require.Equal(t, http.StatusUnauthorized, status(err))
	got, err := w.svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: goodCode})
	require.NoError(t, err)
	require.Len(t, got.Codes, 10)
	require.NotEqual(t, first, got.Codes)
}

func TestRegeneratingWhileOffIsConflict(t *testing.T) {
	w := newWorld()
	ctx := w.as(w.store.person(w.tenant))
	_, err := w.svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: goodCode})
	require.Equal(t, http.StatusConflict, status(err))
	require.Equal(t, "mfa_not_enrolled", code(err))
}

func TestACallerWithoutCredentialsOrWithoutAnAccountIsRefused(t *testing.T) {
	w := newWorld()
	_, err := w.svc.Status(context.Background())
	require.Equal(t, http.StatusUnauthorized, status(err))
	_, err = w.svc.Status(w.as(uuid.New()))
	require.Equal(t, http.StatusNotFound, status(err), "signed in, but no membership behind it")
}
