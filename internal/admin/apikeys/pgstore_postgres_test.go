//go:build integration

package apikeys

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/testkit"
)

func TestPgStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) fixture {
		pool := testkit.NewPostgres(t)
		return fixture{
			Store: NewPgStore(pool),
			member: func(tenant uuid.UUID) uuid.UUID {
				m, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{
					TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "password-1"})
				require.NoError(t, err)
				return m.ID
			},
		}
	})
}
