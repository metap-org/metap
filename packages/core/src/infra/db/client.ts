import { drizzle } from "drizzle-orm/node-postgres";
import pg from "pg";
import * as schema from "./schema";

const { Pool } = pg;

export function describeDatabaseUrl(databaseUrl: string): string {
  const url = new URL(databaseUrl);
  const dbName = url.pathname.replace(/^\//, "");
  return `${url.hostname}:${url.port || "5432"}/${dbName}`;
}

export function createDatabase(databaseUrl: string) {
  const pool = new Pool({ connectionString: databaseUrl });
  const client = drizzle(pool, { schema });

  console.log(`[db] connected: ${describeDatabaseUrl(databaseUrl)}`);

  return {
    client,
    pool,
    async close() {
      await pool.end();
    },
  };
}

export type Database = ReturnType<typeof createDatabase>;
export type DbExecutor = Parameters<Parameters<Database["client"]["transaction"]>[0]>[0];
