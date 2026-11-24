package kernel

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/tenancy"
)

// IdentityDB is the identity.DBTX a slice's store holds. Each statement runs on the transaction of
// the unit of work in its context when there is one on the shared database (so an identity change
// joins the unit and takes no connection of its own), and on the pool otherwise.
//
// That is what makes an identity write safe inside a step: a store that held the pool would ask it for
// a second connection while the step holds the first, which deadlocks a small pool (the family of
// bugs of metap #88 and #89).
type IdentityDB struct{ Pool identity.DBTX }

func (d IdentityDB) target(ctx context.Context) identity.DBTX {
	if tx := tenancy.SharedTx(ctx); tx != nil {
		return tx
	}
	return d.Pool
}

func (d IdentityDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.target(ctx).Exec(ctx, sql, args...)
}

func (d IdentityDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return d.target(ctx).Query(ctx, sql, args...)
}

func (d IdentityDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return d.target(ctx).QueryRow(ctx, sql, args...)
}

var _ identity.DBTX = IdentityDB{}
