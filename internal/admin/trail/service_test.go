package trail

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

// fakeStore is a Store that answers with the rows it was given and remembers what it was asked.
type fakeStore struct {
	rows   []audit.Row
	asked  audit.SearchQuery
	tenant uuid.UUID
	report audit.ChainReport
}

func (f *fakeStore) Search(_ context.Context, tenant uuid.UUID, q audit.SearchQuery) ([]audit.Row, error) {
	f.tenant, f.asked = tenant, q
	return f.rows, nil
}

func (f *fakeStore) VerifyChain(_ context.Context, tenant uuid.UUID) (audit.ChainReport, error) {
	f.tenant = tenant
	return f.report, nil
}

func as(tenant uuid.UUID, roles ...string) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: tenant.String(), UserID: uuid.NewString(), Roles: roles, HasRoles: true})
}

func status(err error) int {
	if se, ok := crud.AsServiceError(err); ok {
		return int(se.Status)
	}
	return 0
}

func rowsAt(n int) []audit.Row {
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	out := make([]audit.Row, n)
	for i := range out {
		out[i] = audit.Row{ID: uuid.New(), Entity: "metadata.users", RecordID: uuid.New(), Action: "update",
			OccurredAt: base.Add(-time.Duration(i) * time.Minute), Diff: []byte(`{}`)}
	}
	return out
}

func TestFiltersBecomeAQuery(t *testing.T) {
	actor, rec := uuid.New(), uuid.New()
	q, bad := SearchFromRequest(&SearchAuditRequest{
		ActorUserId: actor.String(), Entity: "metadata.users", RecordId: rec.String(),
		Actions: []string{" update ", "update", "", "create"}, From: "2026-01-01T00:00:00Z", To: "2026-02-01T00:00:00Z", Limit: 10})
	require.Empty(t, bad)
	require.Equal(t, actor, *q.ActorUserID)
	require.Equal(t, rec, *q.RecordID)
	require.Equal(t, []string{"update", "create"}, q.Actions, "blanks and repeats are dropped")
	require.True(t, q.From.Before(*q.To))
	require.Equal(t, 10, q.Limit)
}

func TestTheFirstMalformedFilterIsNamed(t *testing.T) {
	for req, want := range map[*SearchAuditRequest]string{
		{ActorUserId: "x"}:    "`actorUserId` must be a user id.",
		{RecordId: "x"}:       "`recordId` must be a record id.",
		{From: "yesterday"}:   "`from` must be an RFC 3339 time.",
		{To: "2026-13-45"}:    "`to` must be an RFC 3339 time.",
		{Cursor: "not-valid"}: "Invalid cursor.",
	} {
		_, bad := SearchFromRequest(req)
		require.Equal(t, want, bad)
	}
}

func TestPageSize(t *testing.T) {
	require.Equal(t, 50, pageSize(0))
	require.Equal(t, 50, pageSize(-1))
	require.Equal(t, 50, pageSize(audit.MaxPageSize+1))
	require.Equal(t, 7, pageSize(7))
	require.Equal(t, audit.MaxPageSize, pageSize(audit.MaxPageSize))
}

func TestAPageHasANextCursorOnlyWhenThereIsMore(t *testing.T) {
	rows := rowsAt(4)
	got, next := page(rows, 3)
	require.Len(t, got, 3)
	cur, ok := audit.DecodeCursor(next)
	require.True(t, ok)
	require.Equal(t, got[2].ID, cur.ID, "the cursor is the last row shown")

	got, next = page(rows[:3], 3)
	require.Len(t, got, 3)
	require.Empty(t, next, "exactly a page is the last page")
}

func TestViewCarriesTheOptionalFieldsOnlyWhenTheyAreThere(t *testing.T) {
	bare := View(audit.Row{ID: uuid.New(), Diff: []byte(`{"a":1}`)})
	require.Empty(t, bare.TransitionAction)
	require.Empty(t, bare.ActorUserId)
	require.Zero(t, bare.Seq)
	require.Equal(t, `{"a":1}`, bare.DiffJson)

	actor, seq, reason, tr := uuid.New(), int64(9), "because", "zone.verified"
	full := View(audit.Row{ID: uuid.New(), ActorUserID: &actor, Seq: &seq, Reason: &reason, TransitionAction: &tr, Hash: "ab"})
	require.Equal(t, actor.String(), full.ActorUserId)
	require.EqualValues(t, 9, full.Seq)
	require.Equal(t, "because", full.Reason)
	require.Equal(t, "zone.verified", full.TransitionAction)
	require.Equal(t, "ab", full.Hash)
}

func TestOnlyAnAdminSearchesAndVerifies(t *testing.T) {
	tenant := uuid.New()
	svc := New(&fakeStore{})
	_, err := svc.Search(as(tenant), &SearchAuditRequest{})
	require.Equal(t, http.StatusForbidden, status(err))
	_, err = svc.Verify(as(tenant, "analyst"))
	require.Equal(t, http.StatusForbidden, status(err))
	_, err = svc.Search(context.Background(), &SearchAuditRequest{})
	require.Equal(t, http.StatusUnauthorized, status(err))
}

func TestSearchIsScopedToTheCallersTenantAndPaged(t *testing.T) {
	tenant := uuid.New()
	store := &fakeStore{rows: rowsAt(6)}
	got, err := New(store).Search(as(tenant, "admin"), &SearchAuditRequest{Limit: 5})
	require.NoError(t, err)
	require.Equal(t, tenant, store.tenant, "never another tenant's trail")
	require.Equal(t, 5, store.asked.Limit)
	require.Len(t, got.Entries, 5)
	require.NotEmpty(t, got.NextCursor)
}

func TestABadFilterIsA400AndABadCursorHasItsOwnCode(t *testing.T) {
	svc := New(&fakeStore{})
	ctx := as(uuid.New(), "admin")
	_, err := svc.Search(ctx, &SearchAuditRequest{From: "yesterday"})
	require.Equal(t, http.StatusBadRequest, status(err))
	se, _ := crud.AsServiceError(err)
	require.Equal(t, "validation_failed", se.Code)
	_, err = svc.Search(ctx, &SearchAuditRequest{Cursor: "junk"})
	se, _ = crud.AsServiceError(err)
	require.Equal(t, "invalid_cursor", se.Code)
}

func TestWithoutATrailNothingIsFoundAndTheChainIsFine(t *testing.T) {
	svc := New(nil)
	ctx := as(uuid.New(), "admin")
	got, err := svc.Search(ctx, &SearchAuditRequest{})
	require.NoError(t, err)
	require.Empty(t, got.Entries)
	rep, err := svc.Verify(ctx)
	require.NoError(t, err)
	require.True(t, rep.Ok)
}

func TestVerifyReportsWhatTheChainSays(t *testing.T) {
	tenant := uuid.New()
	store := &fakeStore{report: audit.ChainReport{OK: false, Checked: 12, BrokenSeq: 7, Reason: "hash", HeadSeq: 12, HeadHash: "ff"}}
	rep, err := New(store).Verify(as(tenant, "admin"))
	require.NoError(t, err)
	require.Equal(t, tenant, store.tenant)
	require.Equal(t, &AuditChainReport{Ok: false, Checked: 12, BrokenSeq: 7, Reason: "hash", HeadSeq: 12, HeadHash: "ff"}, rep)
}
