//go:build integration

package mfa

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/secrets"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
)

func outboxAudits(t *testing.T, pool *pgxpool.Pool, member uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM outbox_events WHERE topic = $1 AND aggregate_id = $2", audit.Topic, member).Scan(&n))
	return n
}

// With a sink, a change and its audit entry are one transaction, yet a wrong code still counts: the
// refusal commits the attempt count (or guessing would never be stopped) and writes no entry, and the
// right code writes its entry with the change.
func TestAWrongCodeStillCountsAndARightOneIsAuditedWithItsChange(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	small, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(small.Close)
	router := tenancy.NewRouter(small, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(small)), secrets.EnvStore{})

	tenant := uuid.New()
	m, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{
		TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "password-1"})
	require.NoError(t, err)
	svc := New(NewPgStore(kernel.IdentityDB{Pool: small}), nil, nil).WithSink(action.NewSink(router))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = kernel.WithCaller(ctx, &permission.RequestContext{TenantID: tenant.String(), UserID: m.ID.String()})

	enrol, err := svc.Begin(ctx)
	require.NoError(t, err)
	code, err := identity.TOTPAt(enrol.Secret, time.Now())
	require.NoError(t, err)
	_, err = svc.Confirm(ctx, &MfaCodeRequest{Code: code})
	require.NoError(t, err)
	require.Equal(t, 1, outboxAudits(t, pool, m.ID), "turning it on is audited with the change")

	for i := 0; i < 5; i++ {
		_, err := svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: "000000"})
		se, ok := crud.AsServiceError(err)
		require.True(t, ok)
		require.EqualValues(t, http.StatusUnauthorized, se.Status)
	}
	_, err = svc.RegenerateRecoveryCodes(ctx, &MfaCodeRequest{Code: "000000"})
	se, ok := crud.AsServiceError(err)
	require.True(t, ok)
	require.EqualValues(t, http.StatusTooManyRequests, se.Status, "the wrong attempts were kept, so the account is locked")
	require.Equal(t, 1, outboxAudits(t, pool, m.ID), "a refused code writes no entry")
}
