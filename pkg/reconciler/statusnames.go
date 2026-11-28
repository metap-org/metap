package reconciler

// Values of metadata.reconciler_entity_status.status (one entity's reconcile state).
const (
	entityStatusActive    = "active"
	entityStatusMigrating = "migrating"
	entityStatusError     = "error"
)

// sqlLit renders a status constant as an SQL string literal. The inputs are the compile-time
// constants above, never request data, so splicing them into a statement is safe and keeps one
// spelling of each value for Go and SQL alike.
func sqlLit(s string) string { return "'" + s + "'" }
