// Never lets a DB hiccup at boot become a crash — drift detection is best-effort visibility, not
// a startup precondition, so Check logs and returns nothing; every error path is caught
// internally.
package peripherals

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
)

func Check(ctx context.Context, pool *pgxpool.Pool, entities []metadata.EntitySummary) {
	if err := checkInner(ctx, pool, entities); err != nil {
		slog.Warn("metadata drift check skipped, could not reach the database", "error", err)
	}
}

func checkInner(ctx context.Context, pool *pgxpool.Pool, entities []metadata.EntitySummary) error {
	for _, entity := range entities {
		var existing *string
		row := pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entity.Name)
		if err := row.Scan(&existing); err != nil && err != pgx.ErrNoRows {
			return err
		}

		switch {
		case existing == nil:
			slog.Info("metadata: first boot, recording initial hash", "entity", entity.Name, "hash", entity.Version)
		case *existing != entity.Version:
			slog.Warn("metadata drift detected since last boot",
				"entity", entity.Name, "old_hash", *existing, "new_hash", entity.Version)
		}

		if _, err := pool.Exec(ctx,
			`INSERT INTO metadata_versions (entity_name, hash) VALUES ($1, $2)
			 ON CONFLICT (entity_name) DO UPDATE SET hash = $2, updated_at = now()`,
			entity.Name, entity.Version,
		); err != nil {
			return err
		}
	}
	return nil
}
