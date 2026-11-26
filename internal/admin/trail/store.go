package trail

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
)

// Store is the read side of the audit trail the service needs; audit.Store (PostgresStore in
// production) is one. nil means no trail is kept, and the service answers "nothing found" and "the
// chain is fine".
type Store interface {
	Search(ctx context.Context, tenant uuid.UUID, q audit.SearchQuery) ([]audit.Row, error)
	VerifyChain(ctx context.Context, tenant uuid.UUID) (audit.ChainReport, error)
}
