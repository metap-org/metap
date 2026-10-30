// Index reconciliation for the generic shared "records" table. CREATE INDEX CONCURRENTLY never
// blocks concurrent reads/writes, so this is safe to run on every boot; one field's index
// failing to build must not abort reconciliation for every other field that comes after it.
//
// FINDING, carried over faithfully from the Rust original rather than silently fixed: this
// module's DDL unconditionally targets a table literally named "records" with a WHERE entity =
// '<name>' predicate — the shared generic JSONB table from before table-per-entity. That table
// was dropped entirely by migrations/0033_drop_records_table.sql, and
// metap-metadata's compiler has rejected table_name == "records" for any entity since before
// that migration landed. So in the current schema, every CREATE INDEX this module issues fails
// with "relation records does not exist" — caught and logged by the same per-field error
// handling this file already has for other failures (a missing pg_trgm extension, a name
// collision), so it never crashes a boot, but it also never builds a real index today. This is
// pre-existing dead code in the Rust crate, confirmed by reading migrations/0033_*.sql and
// crates/metap-metadata/src/compiler.rs's table_name_ok check — not a bug introduced by this
// port, and not fixed here, since fixing a pre-existing, unrelated issue is out of scope for a
// faithful port. metap-reconciler's own compile/diff/executor pipeline is what actually builds
// per-entity indexes today (see that crate's own doc comments).
package peripherals

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
)

func buildIndexName(entityName, fieldName, kind string) string {
	prefix := "idx_records"
	switch kind {
	case "uniq":
		prefix = "uniq_records"
	case "gin":
		prefix = "gin_records"
	case "trgm":
		prefix = "trgm_records"
	}
	return prefix + "_" + strings.ReplaceAll(entityName, ".", "_") + "_" + fieldName
}

// Postgres DDL statements (CREATE INDEX) do not support bind parameters at all — entityName/
// fieldName are safe to inline as quoted literals here only because they come exclusively from
// server-authored, compiler-validated metadata, never from request input.
func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func Reconcile(ctx context.Context, pool *pgxpool.Pool, entities []metadata.EntitySummary) {
	for _, entity := range entities {
		for _, field := range entity.Fields {
			if field.Indexed != nil && *field.Indexed {
				logIfFailed(ensureIndex(ctx, pool, entity.Name, field.Name, false), entity.Name, field.Name, "indexed")
			}
			if field.Unique != nil && *field.Unique {
				logIfFailed(ensureIndex(ctx, pool, entity.Name, field.Name, true), entity.Name, field.Name, "unique")
			}
			if field.SearchMode != nil && *field.SearchMode == "fts" {
				logIfFailed(ensureGinIndex(ctx, pool, entity.Name, field.Name), entity.Name, field.Name, "fts")
			}
			// "substring" is the default search_mode when searchable: true — matches
			// query_planner's "fts is the only other option" branching.
			if field.Searchable != nil && *field.Searchable && (field.SearchMode == nil || *field.SearchMode != "fts") {
				logIfFailed(ensureTrgmIndex(ctx, pool, entity.Name, field.Name), entity.Name, field.Name, "trgm")
			}
		}
	}
}

func logIfFailed(err error, entityName, fieldName, kind string) {
	if err != nil {
		slog.Warn("index reconcile failed for this field — continuing with the rest",
			"entity", entityName, "field", fieldName, "kind", kind, "error", err)
	}
}

func indexExists(ctx context.Context, pool *pgxpool.Pool, indexName string) (bool, error) {
	var exists int
	err := pool.QueryRow(ctx, "SELECT 1 FROM pg_indexes WHERE indexname = $1", indexName).Scan(&exists)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func ensureIndex(ctx context.Context, pool *pgxpool.Pool, entityName, fieldName string, unique bool) error {
	kind := "idx"
	if unique {
		kind = "uniq"
	}
	indexName := buildIndexName(entityName, fieldName, kind)

	exists, err := indexExists(ctx, pool, indexName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	fieldLiteral := quoteLiteral(fieldName)
	entityLiteral := quoteLiteral(entityName)
	columns := "(jsonb_extract_path_text(data, " + fieldLiteral + "))"
	uniqueKeyword := ""
	if unique {
		columns = "tenant_id, (jsonb_extract_path_text(data, " + fieldLiteral + "))"
		uniqueKeyword = "UNIQUE "
	}

	sql := "CREATE " + uniqueKeyword + "INDEX CONCURRENTLY IF NOT EXISTS " + quoteIdentifier(indexName) +
		" ON records (" + columns + ") WHERE entity = " + entityLiteral + " AND deleted = false"
	if _, err := pool.Exec(ctx, sql); err != nil {
		return err
	}

	slog.Info("index created", "entity", entityName, "field", fieldName, "unique", unique, "index", indexName)
	return nil
}

func ensureGinIndex(ctx context.Context, pool *pgxpool.Pool, entityName, fieldName string) error {
	indexName := buildIndexName(entityName, fieldName, "gin")

	exists, err := indexExists(ctx, pool, indexName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	fieldLiteral := quoteLiteral(fieldName)
	entityLiteral := quoteLiteral(entityName)
	sql := "CREATE INDEX CONCURRENTLY IF NOT EXISTS " + quoteIdentifier(indexName) +
		" ON records USING GIN (to_tsvector('simple', jsonb_extract_path_text(data, " + fieldLiteral + ")))" +
		" WHERE entity = " + entityLiteral + " AND deleted = false"
	if _, err := pool.Exec(ctx, sql); err != nil {
		return err
	}

	slog.Info("gin index created", "entity", entityName, "field", fieldName, "index", indexName)
	return nil
}

func ensureTrgmIndex(ctx context.Context, pool *pgxpool.Pool, entityName, fieldName string) error {
	indexName := buildIndexName(entityName, fieldName, "trgm")

	exists, err := indexExists(ctx, pool, indexName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	fieldLiteral := quoteLiteral(fieldName)
	entityLiteral := quoteLiteral(entityName)
	sql := "CREATE INDEX CONCURRENTLY IF NOT EXISTS " + quoteIdentifier(indexName) +
		" ON records USING GIN ((jsonb_extract_path_text(data, " + fieldLiteral + ")) gin_trgm_ops)" +
		" WHERE entity = " + entityLiteral + " AND deleted = false"
	if _, err := pool.Exec(ctx, sql); err != nil {
		return err
	}

	slog.Info("trigram index created", "entity", entityName, "field", fieldName, "index", indexName)
	return nil
}
