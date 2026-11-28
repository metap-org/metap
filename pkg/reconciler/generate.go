// Generating migration files. Compile and Diff are pure, so the SQL for a change of the entities
// is worked out against a snapshot of what the previous migration left, with no database: it can
// be committed and reviewed like any other code. Each op also gets the statements that undo it.

package reconciler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/metap-org/metap/pkg/metadata"
)

// SnapshotFormat is the version of the Snapshot file.
const SnapshotFormat = 1

// Snapshot is the schema the migrations generated so far add up to, by table.
type Snapshot struct {
	Format int                       `json:"format"`
	Tables map[string]PhysicalSchema `json:"tables"`
}

// Rename says that a field of the entity in Table was renamed, so its column is renamed instead of
// a new one being added beside it.
type Rename struct{ Table, From, To string }

// GenerateOptions configure Generate.
type GenerateOptions struct {
	Renames []Rename
	// AllowTypeChange writes ALTER COLUMN ... TYPE for a field whose kind changed. It rewrites the
	// table and fails on a value that does not convert; without it such a change is refused.
	AllowTypeChange bool
}

// Script is one generated migration: the statements that apply it and those that undo it.
type Script struct {
	Up, Down []string
	// NoTransaction: the statements cannot run inside a transaction (CONCURRENTLY).
	UpNoTransaction, DownNoTransaction bool
	// Next is the snapshot after this migration.
	Next Snapshot
}

// Empty reports that the entities need no change.
func (s Script) Empty() bool { return len(s.Up) == 0 }

type step struct {
	rank     int
	table    string
	op       DdlOp
	before   PhysicalSchema // the table before this migration; empty for a new one
	newTable bool
	retype   *TypeMismatch
}

// Generate works out the migration that takes prev to what entities need. Like Diff it never
// drops a column or a table: a field or entity that was removed keeps its data.
func Generate(entities []metadata.EntityDefinition, prev Snapshot, opts GenerateOptions) (Script, error) {
	if prev.Format != 0 && prev.Format != SnapshotFormat {
		return Script{}, fmt.Errorf("snapshot format %d is not one this build reads (%d)", prev.Format, SnapshotFormat)
	}
	next := Snapshot{Format: SnapshotFormat, Tables: map[string]PhysicalSchema{}}
	schemas := map[string]bool{}
	for table, s := range prev.Tables {
		next.Tables[table] = s
		schemas[schemaOf(table)] = true
	}

	desired := map[string]PhysicalSchema{}
	for _, e := range entities {
		d, err := Compile(e)
		if err != nil {
			return Script{}, err
		}
		if _, dup := desired[d.Table]; dup {
			return Script{}, fmt.Errorf("two entities use the table %s", d.Table)
		}
		desired[d.Table] = d
	}

	used := map[Rename]bool{}
	var steps []step
	for _, table := range sortedKeys(desired) {
		want := desired[table]
		var have *PhysicalSchema
		before := EmptySchema(table)
		if s, ok := prev.Tables[table]; ok {
			before = clonePhysicalSchema(s)
			have = &before
		}
		var renames [][2]string
		for _, r := range opts.Renames {
			if r.Table == table {
				renames = append(renames, [2]string{r.From, r.To})
			}
		}
		mismatches := ColumnTypeMismatches(want, have)
		if len(mismatches) > 0 && !opts.AllowTypeChange {
			return Script{}, fmt.Errorf("%s: %w: %v (generate with --allow-type-change to write ALTER COLUMN TYPE, then review it)", table, ErrColumnTypeChanged, mismatches)
		}
		for i := range mismatches {
			steps = append(steps, step{rank: rankTypeChange, table: table, before: before, retype: &mismatches[i]})
		}
		for _, op := range Diff(want, have, renames) {
			if op.Kind == OpRenameColumn {
				used[Rename{table, op.RenameFrom, op.RenameTo}] = true
			}
			steps = append(steps, step{rank: opRank(op), table: table, op: op, before: before, newTable: have == nil})
		}

		// What the table holds afterwards: what is wanted, and the columns nothing drops.
		after := clonePhysicalSchema(want)
		for name, column := range before.Columns {
			if _, ok := after.Columns[name]; !ok && !renamedFrom(used, table, name) {
				after.Columns[name] = column
			}
		}
		next.Tables[table] = after
	}
	for _, r := range opts.Renames {
		if !used[r] {
			return Script{}, fmt.Errorf("rename %s:%s=%s matches nothing: the table needs a column %q and a field %q without a column yet", r.Table, r.From, r.To, r.From, r.To)
		}
	}
	// One order across every table, so every table exists before a foreign key points at it.
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].rank < steps[j].rank })

	script := Script{Next: next}
	for _, s := range steps {
		if !s.newTable && (s.op.Kind == OpCreateIndexConcurrently || s.op.Kind == OpDropIndexConcurrently) && s.retype == nil {
			script.UpNoTransaction = true
		}
	}
	newSchemas := map[string]bool{}
	for _, s := range steps {
		if s.newTable && !schemas[schemaOf(s.table)] {
			newSchemas[schemaOf(s.table)] = true
		}
	}
	for _, schema := range sortedKeys(newSchemas) {
		script.Up = append(script.Up, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(schema))
	}
	for _, s := range steps {
		up, err := s.up(script.UpNoTransaction)
		if err != nil {
			return Script{}, err
		}
		script.Up = append(script.Up, up...)
	}
	// Undoing is not the up list backwards: an old index names the old column, so it can only come
	// back after the column has its old name and type again. Hence phases, newest step first within
	// each.
	var phases [downPhases][]string
	for i := len(steps) - 1; i >= 0; i-- {
		down, concurrent, err := steps[i].down()
		if err != nil {
			return Script{}, err
		}
		phase := steps[i].downPhase()
		phases[phase] = append(phases[phase], down...)
		script.DownNoTransaction = script.DownNoTransaction || concurrent
	}
	for _, statements := range phases {
		script.Down = append(script.Down, statements...)
	}
	return script, nil
}

const (
	downDropNewConstraints = iota // new foreign keys, unique constraints and indexes
	downRetype
	downDropColumns
	downRename
	downRestore // what the migration dropped
	downDropTables
	downPhases
)

func (s step) downPhase() int {
	switch {
	case s.retype != nil:
		return downRetype
	case s.newTable && s.op.Kind == OpCreateTable:
		return downDropTables
	}
	switch s.op.Kind {
	case OpAddColumn:
		return downDropColumns
	case OpRenameColumn:
		return downRename
	case OpDropForeignKey, OpDropUnique, OpDropIndexConcurrently:
		return downRestore
	default:
		return downDropNewConstraints
	}
}

// renameSQL renames a column only while it still has its old name, so a script that stopped
// part-way can run again.
func renameSQL(table, from, to string) string {
	schema, bare, _ := strings.Cut(table, ".")
	return fmt.Sprintf("DO $$ BEGIN\n  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = %s AND table_name = %s AND column_name = %s) THEN\n    ALTER TABLE %s RENAME COLUMN %s TO %s;\n  END IF;\nEND $$",
		quoteLiteral(schema), quoteLiteral(bare), quoteLiteral(from), quoteQualifiedIdent(table), quoteIdent(from), quoteIdent(to))
}

func renamedFrom(used map[Rename]bool, table, column string) bool {
	for r := range used {
		if r.Table == table && r.From == column {
			return true
		}
	}
	return false
}

func retypeSQL(table, column, sqlType string) string {
	c := quoteIdent(column)
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::text::%s", quoteQualifiedIdent(table), c, sqlType, c, sqlType)
}

func dropIndexSQL(table, name string) string {
	return fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s.%s", quoteIdent(schemaOf(table)), quoteIdent(name))
}

func dropConstraintSQL(table, name string) string {
	return fmt.Sprintf("ALTER TABLE IF EXISTS %s DROP CONSTRAINT IF EXISTS %s", quoteQualifiedIdent(table), quoteIdent(name))
}

// plain is statements without CONCURRENTLY, for a table nothing is using yet.
func plain(statements []string) []string {
	out := make([]string, len(statements))
	for i, s := range statements {
		out[i] = strings.Replace(s, " INDEX CONCURRENTLY ", " INDEX ", 1)
	}
	return out
}

func (s step) up(noTransaction bool) ([]string, error) {
	if s.retype != nil {
		return []string{"-- Rewrites the table under an exclusive lock and fails on a value that does not convert.\n" +
			retypeSQL(s.table, s.retype.Column, s.retype.Want)}, nil
	}
	if s.op.Kind == OpRenameColumn {
		return []string{renameSQL(s.table, s.op.RenameFrom, s.op.RenameTo)}, nil
	}
	sql := buildSQL(s.table, s.op)
	if len(sql) == 0 || s.op.Kind == OpDropSyncTrigger {
		return nil, fmt.Errorf("%s: op %d has no place in a generated migration (a table from before native columns is moved with dev-tools migrate-to-native-columns)", s.table, s.op.Kind)
	}
	if s.newTable {
		return plain(sql), nil
	}
	// Without a transaction a statement may run again after a failure, so it must tolerate its own
	// leftovers: an invalid index of a failed CONCURRENTLY, a constraint added just before a crash.
	if noTransaction {
		switch s.op.Kind {
		case OpCreateIndexConcurrently:
			sql = append([]string{dropIndexSQL(s.table, s.op.IndexName)}, sql...)
		case OpAddForeignKeyNotValid:
			sql = append([]string{dropConstraintSQL(s.table, s.op.FkName)}, sql...)
		case OpAddUnique:
			sql = append([]string{dropConstraintSQL(s.table, s.op.UniqueName)}, sql...)
		}
	}
	if s.op.IsDestructive() {
		sql[0] = "-- DESTRUCTIVE: removes a constraint or a uniqueness guarantee.\n" + sql[0]
	}
	return sql, nil
}

// down is what undoes the step. concurrent reports that a statement cannot run in a transaction.
func (s step) down() (statements []string, concurrent bool, err error) {
	t := quoteQualifiedIdent(s.table)
	if s.retype != nil {
		return []string{retypeSQL(s.table, s.retype.Column, s.retype.Have)}, false, nil
	}
	if s.newTable {
		switch s.op.Kind {
		case OpCreateTable:
			return []string{"-- DELETES DATA: the table and every row in it.\nDROP TABLE IF EXISTS " + t}, false, nil
		case OpAddForeignKeyNotValid:
			// Dropped before any table is, so two tables that refer to each other can both go.
			return []string{dropConstraintSQL(s.table, s.op.FkName)}, false, nil
		default:
			return nil, false, nil // goes with the table
		}
	}
	switch s.op.Kind {
	case OpAddColumn:
		return []string{fmt.Sprintf("-- DELETES DATA: every value of this column.\nALTER TABLE %s DROP COLUMN IF EXISTS %s", t, quoteIdent(s.op.ColumnName))}, false, nil
	case OpRenameColumn:
		return []string{renameSQL(s.table, s.op.RenameTo, s.op.RenameFrom)}, false, nil
	case OpCreateIndexConcurrently:
		return []string{dropIndexSQL(s.table, s.op.IndexName)}, true, nil
	case OpDropIndexConcurrently:
		old, ok := s.before.Indexes[s.op.IndexName]
		if !ok {
			return nil, false, fmt.Errorf("%s: the snapshot does not know index %s", s.table, s.op.IndexName)
		}
		return []string{dropIndexSQL(s.table, s.op.IndexName), buildCreateIndexSQL(s.table, s.op.IndexName, old)}, true, nil
	case OpAddForeignKeyNotValid:
		return []string{dropConstraintSQL(s.table, s.op.FkName)}, false, nil
	case OpDropForeignKey:
		old, ok := s.before.ForeignKeys[s.op.FkName]
		if !ok {
			return nil, false, fmt.Errorf("%s: the snapshot does not know foreign key %s", s.table, s.op.FkName)
		}
		return []string{dropConstraintSQL(s.table, s.op.FkName), buildAddFkSQL(s.table, s.op.FkName, old),
			fmt.Sprintf("ALTER TABLE %s VALIDATE CONSTRAINT %s", t, quoteIdent(s.op.FkName))}, false, nil
	case OpAddUnique:
		return []string{dropConstraintSQL(s.table, s.op.UniqueName)}, false, nil
	case OpDropUnique:
		old, ok := s.before.Uniques[s.op.UniqueName]
		if !ok {
			return nil, false, fmt.Errorf("%s: the snapshot does not know unique constraint %s", s.table, s.op.UniqueName)
		}
		return []string{dropConstraintSQL(s.table, s.op.UniqueName), buildAddUniqueSQL(s.table, s.op.UniqueName, old)}, false, nil
	case OpValidateForeignKey:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("%s: no way to undo op %d", s.table, s.op.Kind)
	}
}
