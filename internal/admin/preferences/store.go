package preferences

import (
	"context"

	"github.com/google/uuid"
)

// Store is where a member's preferences live. The service depends on this, not on a database, so
// its tests run without one; PgStore is the production implementation.
type Store interface {
	// Locale is the member's locale; DefaultLocale when none was ever saved.
	Locale(ctx context.Context, tenant, user uuid.UUID) (string, error)
	SetLocale(ctx context.Context, tenant, user uuid.UUID, locale string) error
}
