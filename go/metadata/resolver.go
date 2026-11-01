package metadata

import (
	"context"

	"github.com/google/uuid"
)

// MetadataResolver resolves the metadata registry a given tenant should see — the pluggable
// per-tenant metadata seam (feature 15). nil (the default everywhere this isn't configured)
// means every tenant keeps sharing one global registry, i.e. today's behavior, unchanged.
type MetadataResolver interface {
	// Resolve returns the registry tenantID should see right now.
	Resolve(ctx context.Context, tenantID uuid.UUID) (*MetadataRegistry, error)
	// Invalidate drops any cached registry for tenantID, forcing the next Resolve to rebuild
	// it. A resolver with no cache of its own can leave this a no-op.
	Invalidate(ctx context.Context, tenantID uuid.UUID)
}
