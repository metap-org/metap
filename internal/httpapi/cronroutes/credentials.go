package cronroutes

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// missingCredential is the first id of ids that is not one of tenantID's credentials.
func missingCredential(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, ids []uuid.UUID) (uuid.UUID, bool, error) {
	for _, id := range ids {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM metadata.tenant_credentials WHERE tenant_id = $1 AND id = $2)`,
			tenantID, id).Scan(&exists); err != nil {
			return uuid.Nil, false, err
		}
		if !exists {
			return id, true, nil
		}
	}
	return uuid.Nil, false, nil
}
