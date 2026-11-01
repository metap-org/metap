// A pure, DB-free "what would Reconcile run" preview — Plan(desired, actual, renames) calls the
// same Diff the real apply path uses and renders each resulting DdlOp to the exact SQL Execute
// would run, without ever opening a connection. Lets a human review generated DDL before applying
// it by hand — the same split modern migration tools make — as an addition alongside, never a
// replacement for, the auto-apply Reconcile every downstream binary's boot sequence relies on.
package reconciler

// PlannedOp is one planned DDL operation and the literal SQL Execute would run for it. SQL is
// nil for OpBackfillColumn — that op is a checkpointed batch loop, not a single SQL statement,
// so there is nothing meaningful to print; a caller rendering a plan for a human should say so
// explicitly instead of showing nothing.
type PlannedOp struct {
	Op  DdlOp
	SQL []string
}

// Plan has the same signature as Diff, plus it renders the SQL each op would run against
// desired.Table.
func Plan(desired PhysicalSchema, actual *PhysicalSchema, renames [][2]string) []PlannedOp {
	ops := Diff(desired, actual, renames)
	planned := make([]PlannedOp, len(ops))
	for i, op := range ops {
		planned[i] = PlannedOp{Op: op, SQL: buildSQL(desired.Table, op)}
	}
	return planned
}
