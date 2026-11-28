//go:build integration

package app

import (
	"bytes"
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/reconciler"
	"github.com/metap-org/metap/pkg/testkit"
)

// The command as a person runs it, on the single connection it opens: anything it did on a second
// one while holding the first would hang here instead of passing on a test's larger pool.
func TestTheMigrateCommandAdoptsAnExistingDatabase(t *testing.T) {
	pool := testkit.NewPostgres(t)
	u, err := url.Parse(testkit.BaseDatabaseURL())
	require.NoError(t, err)
	u.Path = "/" + pool.Config().ConnConfig.Database
	t.Setenv("DATABASE_URL", u.String())

	entities := shop(firstItemFields())
	for _, e := range []metadata.EntityDefinition{entities[1], entities[0]} {
		_, err := reconciler.Reconcile(context.Background(), pool, uuid.Nil, e, nil)
		require.NoError(t, err)
	}
	dir := t.TempDir()
	generate(t, dir, "init", entities, reconciler.GenerateOptions{})
	opts := serviceOptions(dir, entities)
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var out bytes.Buffer
		err := MigrateCommand(ctx, opts, args, &out)
		return out.String(), err
	}
	ledger := func() bool {
		var ok bool
		require.NoError(t, pool.QueryRow(context.Background(), "SELECT to_regclass('metadata.metap_product_migrations') IS NOT NULL").Scan(&ok))
		return ok
	}

	_, err = run("status")
	require.ErrorIs(t, err, ErrSchemaNotReady)
	_, err = run("up", "--dry-run")
	require.NoError(t, err)
	_, err = run("baseline")
	require.ErrorContains(t, err, "repeat with --yes")
	assert.False(t, ledger(), "status, up --dry-run and baseline without --yes change nothing")

	_, err = run("baseline", "--yes")
	require.NoError(t, err)
	out, err := run("status")
	require.NoError(t, err)
	assert.Contains(t, out, "up to date")

	out, err = run("down")
	require.ErrorIs(t, err, errNotConfirmed)
	assert.Contains(t, out, `DROP TABLE IF EXISTS "migt"."mig_items"`)
	_, err = run("status")
	require.NoError(t, err, "down without --yes ran nothing")
	_, err = run("down", "--yes")
	require.NoError(t, err)
	_, err = run("up")
	require.NoError(t, err)
	_, err = run("status")
	require.NoError(t, err)
}
