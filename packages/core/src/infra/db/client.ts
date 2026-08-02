import { drizzle } from "drizzle-orm/node-postgres";
import pg from "pg";
import * as schema from "./schema";

const { Pool } = pg;

export function createDatabase(databaseUrl: string) {
  const pool = new Pool({ connectionString: databaseUrl });
  const client = drizzle(pool, { schema });

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
