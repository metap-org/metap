package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Require is the boot check of a binary that only uses the platform's tables: it fails, saying
// what to run, when any of fsys's migrations is not applied. It changes nothing.
func Require(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	state, err := PlatformState(ctx, pool, fsys)
	if err != nil {
		return err
	}
	if len(state.Pending) == 0 {
		return nil
	}
	names := make([]string, 0, len(state.Pending))
	for _, m := range state.Pending {
		names = append(names, m.Filename)
	}
	if len(names) > 10 {
		names = append(names[:10], fmt.Sprintf("and %d more", len(names)-10))
	}
	return fmt.Errorf("the database lacks platform migrations (%s): run `db-migrate up` against it first", strings.Join(names, ", "))
}
