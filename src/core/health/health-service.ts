import { sql } from "drizzle-orm";
import type { Database } from "../../infra/db/client";

export class HealthService {
  constructor(private readonly db: Database) {}

  async checkDatabase() {
    try {
      await this.db.client.execute(sql`select 1`);
      return true;
    } catch {
      return false;
    }
  }
}
