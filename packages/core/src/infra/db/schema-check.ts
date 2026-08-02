import { sql } from "drizzle-orm";
import type { Database } from "./client";

const REQUIRED_TABLES = [
  "records",
  "policies",
  "outbox_events",
  "workflow_events",
  "metadata_versions",
  "user_roles",
] as const;

export async function assertCoreSchemaPresent(db: Database): Promise<void> {
  const result = await db.client.execute<{ table_name: string }>(sql`
    SELECT table_name FROM information_schema.tables
    WHERE table_schema = 'public' AND table_name IN ${REQUIRED_TABLES}
  `);

  const found = new Set(result.rows.map((row) => row.table_name));
  const missing = REQUIRED_TABLES.filter((table) => !found.has(table));

  if (missing.length > 0) {
    throw new Error(
      `Database is missing expected packages/core tables: ${missing.join(", ")}. ` +
        `Did DATABASE_URL point at an unmigrated or wrong database? Run "pnpm db:migrate" against the intended database.`,
    );
  }
}
