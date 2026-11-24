//go:build integration

package apikeys

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/secrets"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
)

func auditRows(t *testing.T, pool *pgxpool.Pool, member uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM outbox_events WHERE topic = $1 AND aggregate_id = $2", audit.Topic, member).Scan(&n))
	return n
}

// The key and its audit entry are one transaction on the shared database, for a tenant with a database
// of its own as well as for one without, on a pool of one connection.
func TestTheKeyAndItsAuditEntryCommitAndRollBackTogetherWhateverTheTenantsStrategy(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	small, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(small.Close)
	router := tenancy.NewRouter(small, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(small)), secrets.EnvStore{})

	for name, dedicated := range map[string]bool{"shared database": false, "its own database": true} {
		t.Run(name, func(t *testing.T) {
			tenant := uuid.New()
			if dedicated {
				// The shared unit never opens the tenant's own database, so a secret that points nowhere is fine:
				// a unit on the tenant's database would fail to resolve it.
				_, err := pool.Exec(context.Background(),
					"INSERT INTO control.tenants (id, tier, strategy, dsn_secret_ref, status) VALUES ($1, 'paid', 'dedicated_db', 'no-such-secret', 'active')", tenant)
				require.NoError(t, err)
				t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", tenant) })
			}
			member, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{
				TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "password-1"})
			require.NoError(t, err)
			admin := member.ID

			svc := New(NewPgStore(kernel.IdentityDB{Pool: small}), nil, nil, nil).WithSink(action.NewSink(router))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ctx = kernel.WithCaller(ctx, &permission.RequestContext{
				TenantID: tenant.String(), UserID: admin.String(), Roles: []string{"admin"}, HasRoles: true})

			got, err := svc.Create(ctx, &CreateApiKeyRequest{UserId: admin.String(), Name: "ci", ExpiresInDays: 30})
			require.NoError(t, err)
			require.Equal(t, 1, auditRows(t, pool, admin), "the entry is in the outbox of the same transaction")
			keys, err := identity.ListAPIKeys(context.Background(), pool, tenant, admin)
			require.NoError(t, err)
			require.Len(t, keys, 1)

			// A change that fails after the key is written leaves neither the key nor an entry.
			unit, err := action.NewSharedUnit(action.NewSink(router), &permission.RequestContext{TenantID: tenant.String(), UserID: admin.String()})
			require.NoError(t, err)
			store := NewPgStore(kernel.IdentityDB{Pool: small})
			err = unit.Step(ctx, "doomed", func(ctx context.Context) error {
				if _, _, err := store.Create(ctx, tenant, admin, "doomed", nil, nil, time.Now()); err != nil {
					return err
				}
				unit.AuditPrepared(kernel.AuditEntryOf(tenant, admin, AuditEntityUsers, admin, 0, nil, time.Now()))
				return context.Canceled
			})
			require.Error(t, err)
			keys, err = identity.ListAPIKeys(context.Background(), pool, tenant, admin)
			require.NoError(t, err)
			require.Len(t, keys, 1, "the doomed key was rolled back")
			require.Equal(t, 1, auditRows(t, pool, admin), "and so was its entry")

			require.NoError(t, svc.Revoke(ctx, got.ApiKey.Id))
			require.Equal(t, 2, auditRows(t, pool, admin))
		})
	}
}
