import { eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { metadataVersions } from "../../infra/db/schema";

export class MetadataDriftService {
  constructor(private readonly db: Database) {}

  // Never let a DB hiccup at boot become a crash — this mirrors HealthService's
  // graceful-degradation stance elsewhere in this codebase. Drift detection is
  // best-effort visibility, not a startup precondition.
  async check(
    entities: readonly { name: string; version: string }[],
    log: { warn: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    try {
      for (const entity of entities) {
        const [existing] = await this.db.client
          .select()
          .from(metadataVersions)
          .where(eq(metadataVersions.entityName, entity.name));

        if (!existing) {
          log.warn(
            { entity: entity.name, hash: entity.version },
            "metadata: first boot, recording initial hash",
          );
        } else if (existing.hash !== entity.version) {
          log.warn(
            { entity: entity.name, oldHash: existing.hash, newHash: entity.version },
            "metadata: drift detected since last boot",
          );
        }

        await this.db.client
          .insert(metadataVersions)
          .values({ entityName: entity.name, hash: entity.version })
          .onConflictDoUpdate({
            target: metadataVersions.entityName,
            set: { hash: entity.version, updatedAt: new Date() },
          });
      }
    } catch (error) {
      log.warn({ err: error }, "metadata: drift check skipped, could not reach the database");
    }
  }
}
