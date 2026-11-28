// Files: NNNN_name.sql or NNNN_name.up.sql is a migration, NNNN_name.down.sql (same number and
// name) undoes it.

package migrate

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Directives of a migration file.
const (
	// NoTransactionDirective, as a line of its own, runs the file's statements one by one outside a
	// transaction (CREATE INDEX CONCURRENTLY needs that).
	NoTransactionDirective = "-- migrate:no-transaction"
	// StatementBreak separates the statements of a no-transaction file. A transactional file is sent
	// whole and needs none.
	StatementBreak = "-- migrate:statement-break"
)

// Migration is one parsed migration with the script that undoes it, if it has one.
type Migration struct {
	Version     int64
	Description string
	Filename    string
	SQL         string
	// Down is "" for a migration that cannot be rolled back.
	Down         string
	DownFilename string
	// Checksum is of SQL: an applied migration must not change.
	Checksum [32]byte
}

// NoTransaction reports whether script carries NoTransactionDirective.
func NoTransaction(script string) bool {
	for _, line := range strings.Split(script, "\n") {
		if strings.TrimSpace(line) == NoTransactionDirective {
			return true
		}
	}
	return false
}

// Statements is script cut at StatementBreak, without the parts that hold only comments.
func Statements(script string) []string {
	var out []string
	for _, part := range strings.Split(script, StatementBreak) {
		for _, line := range strings.Split(part, "\n") {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "--") {
				out = append(out, strings.TrimSpace(part))
				break
			}
		}
	}
	return out
}

var filenamePattern = regexp.MustCompile(`^(\d+)_(.+?)(\.up|\.down)?\.sql$`)

// LoadMigrations reads the migrations of dir, sorted by version ascending.
func LoadMigrations(dir string) ([]Migration, error) {
	return LoadMigrationsFS(os.DirFS(dir))
}

// LoadMigrationsFS is LoadMigrations over any fs.FS — e.g. the embedded migrations.FS from the
// repo-root migrations package, so a binary needs no MIGRATIONS_DIR on disk at all. Two
// migrations with one number, and a down script with no migration, are errors.
func LoadMigrationsFS(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	ups := map[int64]Migration{}
	type down struct{ name, file, sql string }
	downs := map[int64]down{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := filenamePattern.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %s: invalid version prefix: %w", entry.Name(), err)
		}
		content, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, err
		}
		name := strings.ReplaceAll(m[2], "_", " ")
		if m[3] == ".down" {
			if other, dup := downs[version]; dup {
				return nil, fmt.Errorf("two down scripts for version %d: %s and %s", version, other.file, entry.Name())
			}
			downs[version] = down{name, entry.Name(), string(content)}
			continue
		}
		if other, dup := ups[version]; dup {
			return nil, fmt.Errorf("two migrations numbered %d: %s and %s", version, other.Filename, entry.Name())
		}
		ups[version] = Migration{Version: version, Description: name, Filename: entry.Name(), SQL: string(content), Checksum: sha256.Sum256(content)}
	}
	for version, d := range downs {
		up, ok := ups[version]
		if !ok || up.Description != d.name {
			return nil, fmt.Errorf("%s has no migration of the same number and name", d.file)
		}
		up.Down, up.DownFilename = d.sql, d.file
		ups[version] = up
	}
	out := make([]Migration, 0, len(ups))
	for _, m := range ups {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
