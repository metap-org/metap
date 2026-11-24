//go:build integration

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/testkit"
)

func TestPgStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) fixture {
		pool := testkit.NewPostgres(t)
		ctx := context.Background()
		return fixture{
			Store: NewPgStore(pool),
			person: func(tenant uuid.UUID) uuid.UUID {
				m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "password-1"})
				require.NoError(t, err)
				return m.ID
			},
			signIn: func(tenant, member uuid.UUID, ttl time.Duration, now time.Time) uuid.UUID {
				id, err := identity.IdentityOf(ctx, pool, tenant, member)
				require.NoError(t, err)
				sid, err := identity.CreateSession(ctx, pool, id, member, tenant, ttl, "203.0.113.7", "test", now)
				require.NoError(t, err)
				return sid
			},
		}
	})
}
