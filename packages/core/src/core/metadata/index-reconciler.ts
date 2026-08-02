import { sql } from "drizzle-orm";
import type { Database } from "../../infra/db/client";

export type IndexableField = {
  name: string;
  indexed?: boolean;
  unique?: boolean;
  searchable?: boolean;
  searchMode?: "substring" | "fts";
};

export type IndexableEntity = {
  name: string;
  fields: readonly IndexableField[];
};

function buildIndexName(
  entityName: string,
  fieldName: string,
  kind: "idx" | "uniq" | "gin",
): string {
  const prefix = kind === "uniq" ? "uniq_records" : kind === "gin" ? "gin_records" : "idx_records";
  return `${prefix}_${entityName.replace(/\./g, "_")}_${fieldName}`;
}

// Postgres DDL statements (CREATE INDEX) do not support bind parameters at
// all — unlike the SELECT below, `CREATE INDEX ... WHERE entity = $1` fails
// outright. entityName/fieldName are safe to inline as quoted literals here
// only because they come exclusively from server-authored, MetadataCompiler-
// validated metadata, never from request input.
function quoteLiteral(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

export class IndexReconciler {
  constructor(private readonly db: Database) {}

  // Never let a DB hiccup at boot become a crash — mirrors MetadataDriftService's
  // graceful-degradation stance. CONCURRENTLY means these statements never block
  // concurrent reads/writes on `records`, so this is safe to run on every boot in
  // every environment (no dev/prod split needed).
  async reconcile(
    entities: readonly IndexableEntity[],
    log: { info: (obj: unknown, msg: string) => void; warn: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    try {
      for (const entity of entities) {
        for (const field of entity.fields) {
          if (field.indexed) {
            await this.ensureIndex(entity.name, field.name, false, log);
          }
          if (field.unique) {
            await this.ensureIndex(entity.name, field.name, true, log);
          }
          if (field.searchMode === "fts") {
            await this.ensureGinIndex(entity.name, field.name, log);
          }
        }
      }
    } catch (error) {
      log.warn({ err: error }, "index: reconcile skipped, could not reach the database");
    }
  }

  private async ensureIndex(
    entityName: string,
    fieldName: string,
    unique: boolean,
    log: { info: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    const indexName = buildIndexName(entityName, fieldName, unique ? "uniq" : "idx");

    // Check first rather than relying solely on IF NOT EXISTS so we only log
    // (and only pay for a CONCURRENTLY build) when something actually changes.
    const existing = await this.db.client.execute(
      sql`SELECT 1 FROM pg_indexes WHERE indexname = ${indexName}`,
    );
    if (existing.rows.length > 0) {
      return;
    }

    const fieldLiteral = sql.raw(quoteLiteral(fieldName));
    const entityLiteral = sql.raw(quoteLiteral(entityName));
    // Must match QueryPlanner's/condition-to-sql.ts's fieldExpression() exactly
    // (jsonb_extract_path_text, not the `->>` operator) — Postgres only uses an
    // expression index when the query's expression is syntactically identical
    // to the indexed one; `data->>'f'` and `jsonb_extract_path_text(data, 'f')`
    // are semantically equal but distinct expressions, so an index built on one
    // form is never selected for a query written in the other (confirmed via
    // EXPLAIN with enable_seqscan off).
    const columns = unique
      ? sql`tenant_id, (jsonb_extract_path_text(data, ${fieldLiteral}))`
      : sql`(jsonb_extract_path_text(data, ${fieldLiteral}))`;
    const uniqueKeyword = unique ? sql`UNIQUE ` : sql``;

    await this.db.client.execute(sql`
      CREATE ${uniqueKeyword}INDEX CONCURRENTLY IF NOT EXISTS ${sql.identifier(indexName)}
      ON records (${columns})
      WHERE entity = ${entityLiteral} AND deleted = false
    `);

    log.info({ entity: entityName, field: fieldName, unique, index: indexName }, "index: created");
  }

  private async ensureGinIndex(
    entityName: string,
    fieldName: string,
    log: { info: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    const indexName = buildIndexName(entityName, fieldName, "gin");

    const existing = await this.db.client.execute(
      sql`SELECT 1 FROM pg_indexes WHERE indexname = ${indexName}`,
    );
    if (existing.rows.length > 0) {
      return;
    }

    const fieldLiteral = sql.raw(quoteLiteral(fieldName));
    const entityLiteral = sql.raw(quoteLiteral(entityName));

    // Must match QueryPlanner's to_tsvector('simple', jsonb_extract_path_text(data, field))
    // expression exactly — same reasoning as ensureIndex above.
    await this.db.client.execute(sql`
      CREATE INDEX CONCURRENTLY IF NOT EXISTS ${sql.identifier(indexName)}
      ON records USING GIN (to_tsvector('simple', jsonb_extract_path_text(data, ${fieldLiteral})))
      WHERE entity = ${entityLiteral} AND deleted = false
    `);

    log.info({ entity: entityName, field: fieldName, index: indexName }, "index: created");
  }
}
