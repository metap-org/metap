// Package migrations embeds this repo's SQL migrations, so binaries (cmd/db-migrate,
// cmd/dev-tools) and tests apply the real files without depending on the working directory.
// Set MIGRATIONS_DIR to override with an on-disk directory instead.
package migrations

import "embed"

// FS holds every *.sql migration file at its root, e.g. "0000_green_jean_grey.sql".
//
//go:embed *.sql
var FS embed.FS
