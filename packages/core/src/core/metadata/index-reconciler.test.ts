import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { IndexReconciler } from "./index-reconciler";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("IndexReconciler (live DB)", () => {
  let db: Database;
  let reconciler: IndexReconciler;
  let pgClient: Client;
  let dbAvailable = true;

  const entityName = "test.index_reconciler_entity";
  const indexedIndexName = `idx_records_${entityName.replace(/\./g, "_")}_region`;
  const uniqueIndexName = `uniq_records_${entityName.replace(/\./g, "_")}_code`;
  const ginIndexName = `gin_records_${entityName.replace(/\./g, "_")}_title`;

  function makeLogSpy() {
    const infoMessages: string[] = [];
    const warnMessages: string[] = [];
    return {
      info: (_obj: unknown, msg: string) => infoMessages.push(msg),
      warn: (_obj: unknown, msg: string) => warnMessages.push(msg),
      infoMessages,
      warnMessages,
    };
  }

  async function dropTestIndexes() {
    await pgClient.query(`DROP INDEX IF EXISTS ${indexedIndexName}`);
    await pgClient.query(`DROP INDEX IF EXISTS ${uniqueIndexName}`);
    await pgClient.query(`DROP INDEX IF EXISTS ${ginIndexName}`);
  }

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    reconciler = new IndexReconciler(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping IndexReconciler live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await dropTestIndexes();
      await pgClient.end();
    }
    await db.close();
  });

  it("creates a partial index for an indexed: true field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const log = makeLogSpy();
    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "region", indexed: true }] }],
      log,
    );

    const { rows } = await pgClient.query<{ indexdef: string }>(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [indexedIndexName],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]?.indexdef).toContain("region");
    expect(log.infoMessages).toContain("index: created");
  });

  it("creates an index Postgres actually uses for QueryPlanner's exact expression form", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "region", indexed: true }] }],
      makeLogSpy(),
    );

    // Regression guard: an earlier version of this reconciler built indexes on
    // `data->>'field'`, which QueryPlanner/condition-to-sql.ts's
    // jsonb_extract_path_text(data, fieldName) form never matches — Postgres
    // only picks an expression index when the query's expression is
    // syntactically identical to the indexed one. Verify actual usage (not
    // just that a pg_indexes row exists) via EXPLAIN with seqscan disabled.
    await pgClient.query("BEGIN");
    try {
      await pgClient.query("SET LOCAL enable_seqscan = off");
      const { rows } = await pgClient.query<{ "QUERY PLAN": string }>(
        "EXPLAIN SELECT id FROM records WHERE entity = $1 AND deleted = false AND jsonb_extract_path_text(data, $2) = $3",
        [entityName, "region", "west"],
      );
      const plan = rows.map((row) => row["QUERY PLAN"]).join("\n");
      expect(plan).toContain(indexedIndexName);
      expect(plan).toContain("Index Cond");
    } finally {
      await pgClient.query("ROLLBACK");
    }
  });

  it("creates a tenant-scoped unique index for a unique: true field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const log = makeLogSpy();
    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "code", unique: true }] }],
      log,
    );

    const { rows } = await pgClient.query<{ indexdef: string }>(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [uniqueIndexName],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]?.indexdef).toContain("UNIQUE");
    expect(rows[0]?.indexdef).toContain("tenant_id");
  });

  it("creates a GIN index for a searchMode: 'fts' field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const log = makeLogSpy();
    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "title", searchable: true, searchMode: "fts" }] }],
      log,
    );

    const { rows } = await pgClient.query<{ indexdef: string }>(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [ginIndexName],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]?.indexdef.toLowerCase()).toContain("gin");
    expect(rows[0]?.indexdef).toContain("to_tsvector");
    expect(log.infoMessages).toContain("index: created");
  });

  it("creates a GIN index Postgres actually uses for QueryPlanner's exact FTS expression", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "title", searchable: true, searchMode: "fts" }] }],
      makeLogSpy(),
    );

    // Same regression class as sub-project 1: verify actual index usage via
    // EXPLAIN, not just that a pg_indexes row exists. The table's existing
    // (tenant_id, entity, ...) composite indexes are cheap enough on a tiny
    // test table that the planner prefers them + a Filter over the GIN index
    // even with seqscan disabled — drop them for the duration of this
    // transaction (rolled back after) so the GIN index is the only viable
    // path and its selection actually proves it's usable.
    await pgClient.query("BEGIN");
    try {
      await pgClient.query("DROP INDEX records_tenant_entity_status_idx");
      await pgClient.query("DROP INDEX records_tenant_entity_created_idx");
      await pgClient.query("SET LOCAL enable_seqscan = off");
      const { rows } = await pgClient.query<{ "QUERY PLAN": string }>(
        `EXPLAIN SELECT id FROM records WHERE entity = $1 AND deleted = false
         AND to_tsvector('simple', jsonb_extract_path_text(data, $2)) @@ plainto_tsquery('simple', $3)`,
        [entityName, "title", "quick brown"],
      );
      const plan = rows.map((row) => row["QUERY PLAN"]).join("\n");
      expect(plan).toContain(ginIndexName);
    } finally {
      await pgClient.query("ROLLBACK");
    }
  });

  it("is idempotent across repeated runs", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const entities = [{ name: entityName, fields: [{ name: "region", indexed: true }] }];
    const first = makeLogSpy();
    await reconciler.reconcile(entities, first);
    const second = makeLogSpy();
    await reconciler.reconcile(entities, second);

    expect(first.infoMessages).toEqual(["index: created"]);
    expect(second.infoMessages).toEqual([]);

    const { rows } = await pgClient.query("SELECT indexdef FROM pg_indexes WHERE indexname = $1", [
      indexedIndexName,
    ]);
    expect(rows).toHaveLength(1);
  });

  it("does not throw when the database is unreachable", async () => {
    const unreachableDb = createDatabase("postgres://x:x@localhost:1/x");
    const unreachableReconciler = new IndexReconciler(unreachableDb);
    const log = makeLogSpy();

    await expect(
      unreachableReconciler.reconcile(
        [{ name: entityName, fields: [{ name: "region", indexed: true }] }],
        log,
      ),
    ).resolves.toBeUndefined();
    expect(log.warnMessages).toEqual(["index: reconcile skipped, could not reach the database"]);

    await unreachableDb.close();
  });
});
