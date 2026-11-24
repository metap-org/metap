//go:build integration

package mfa

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
		return fixture{
			Store: NewPgStore(pool),
			person: func(tenant uuid.UUID) uuid.UUID {
				m, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{
					TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "password-1"})
				require.NoError(t, err)
				return m.ID
			},
			code: func(secret string, at time.Time) string {
				c, err := identity.TOTPAt(secret, at)
				require.NoError(t, err)
				return c
			},
		}
	})
}
