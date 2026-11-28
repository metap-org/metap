// The read-only question a service asks at boot: does this database have what my entities need?

package reconciler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/metadata"
)

// SchemaGap is how one entity's table differs from its definition.
type SchemaGap struct {
	// Missing is what the entity needs and the database lacks: it cannot be served properly.
	Missing []string
	// Extra is what the database has beyond the definition (a newer build's index, an old
	// constraint): nothing is lost by serving, though a leftover constraint is still enforced.
	Extra []string
}

// Check compares entity with the database. It changes nothing.
func Check(ctx context.Context, pool *pgxpool.Pool, entity metadata.EntityDefinition) (SchemaGap, error) {
	var gap SchemaGap
	desired, err := Compile(entity)
	if err != nil {
		return gap, err
	}
	// The tenant only keys the backfill ledger of a table from before native columns, and such a
	// table is reported as missing whatever its progress.
	actual, err := Introspect(ctx, pool, uuid.Nil, entity.Name, desired.Table)
	if err != nil {
		return gap, err
	}
	if actual == nil {
		gap.Missing = []string{"table " + desired.Table}
		return gap, nil
	}
	if actual.HasLegacyData {
		gap.Missing = []string{fmt.Sprintf("%s: %v", desired.Table, ErrLegacyDataColumn)}
		return gap, nil
	}
	for _, m := range ColumnTypeMismatches(desired, actual) {
		gap.Missing = append(gap.Missing, fmt.Sprintf("%s.%s is %s, the field needs %s", desired.Table, m.Column, m.Have, m.Want))
	}
	ops := Diff(desired, actual, nil)
	rebuilt := map[string]bool{}
	for _, op := range ops {
		switch op.Kind {
		case OpCreateIndexConcurrently:
			rebuilt[op.IndexName] = true
		case OpAddForeignKeyNotValid:
			rebuilt[op.FkName] = true
		case OpAddUnique:
			rebuilt[op.UniqueName] = true
		}
	}
	for _, op := range ops {
		switch op.Kind {
		case OpAddColumn:
			gap.Missing = append(gap.Missing, "column "+desired.Table+"."+op.ColumnName)
		case OpCreateIndexConcurrently:
			gap.Missing = append(gap.Missing, "index "+op.IndexName+" on "+desired.Table+" (absent, invalid or different)")
		case OpAddForeignKeyNotValid:
			gap.Missing = append(gap.Missing, "foreign key "+op.FkName+" on "+desired.Table)
		case OpValidateForeignKey:
			if !rebuilt[op.FkName] {
				gap.Missing = append(gap.Missing, "validation of foreign key "+op.FkName+" on "+desired.Table)
			}
		case OpAddUnique:
			gap.Missing = append(gap.Missing, "unique constraint "+op.UniqueName+" on "+desired.Table)
		case OpDropIndexConcurrently:
			if !rebuilt[op.IndexName] {
				gap.Extra = append(gap.Extra, "index "+op.IndexName+" on "+desired.Table)
			}
		case OpDropForeignKey:
			if !rebuilt[op.FkName] {
				gap.Extra = append(gap.Extra, "foreign key "+op.FkName+" on "+desired.Table)
			}
		case OpDropUnique:
			if !rebuilt[op.UniqueName] {
				gap.Extra = append(gap.Extra, "unique constraint "+op.UniqueName+" on "+desired.Table)
			}
		}
	}
	return gap, nil
}
