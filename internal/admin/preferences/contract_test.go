package preferences

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// runStoreContract is what every Store must do, run against the in-memory store (unit tests) and
// against PgStore (integration tests), so the two cannot drift apart.
func runStoreContract(t *testing.T, newStore func(t *testing.T) Store) {
	ctx := context.Background()

	t.Run("nothing saved reads as the default locale", func(t *testing.T) {
		s := newStore(t)
		got, err := s.Locale(ctx, uuid.New(), uuid.New())
		require.NoError(t, err)
		require.Equal(t, DefaultLocale, got)
	})

	t.Run("a saved locale reads back and can be changed", func(t *testing.T) {
		s := newStore(t)
		tenant, user := uuid.New(), uuid.New()
		require.NoError(t, s.SetLocale(ctx, tenant, user, "vi"))
		got, err := s.Locale(ctx, tenant, user)
		require.NoError(t, err)
		require.Equal(t, "vi", got)
		require.NoError(t, s.SetLocale(ctx, tenant, user, "en"))
		got, _ = s.Locale(ctx, tenant, user)
		require.Equal(t, "en", got)
	})

	t.Run("members and tenants do not share a locale", func(t *testing.T) {
		s := newStore(t)
		tenant, a, b := uuid.New(), uuid.New(), uuid.New()
		require.NoError(t, s.SetLocale(ctx, tenant, a, "vi"))
		other, err := s.Locale(ctx, tenant, b)
		require.NoError(t, err)
		require.Equal(t, DefaultLocale, other)
		otherTenant, err := s.Locale(ctx, uuid.New(), a)
		require.NoError(t, err)
		require.Equal(t, DefaultLocale, otherTenant)
	})
}

func TestMemStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(*testing.T) Store { return newMemStore() })
}
