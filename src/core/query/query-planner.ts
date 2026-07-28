import type { SQL } from "drizzle-orm";
import { and, desc, eq } from "drizzle-orm";
import { records } from "../../infra/db/schema";
import type { MetadataRegistry } from "../metadata/metadata-registry";
import type { PermissionService, RequestContext } from "../permission/permission-service";

export type ListInput = {
  limit: number;
  cursor?: string;
  sort?: string;
};

export type PlannedListQuery = {
  where: SQL | undefined;
  limit: number;
  orderBy: SQL[];
};

export class QueryPlanner {
  constructor(
    private readonly metadata: MetadataRegistry,
    private readonly permissions: PermissionService,
  ) {}

  planList(
    entityName: string,
    input: ListInput,
    context: Partial<RequestContext>,
  ): PlannedListQuery {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      throw new Error(`Entity not found: ${entityName}`);
    }

    const tenantId = this.permissions.scopedTenant(context);
    const limit = Math.min(input.limit, entity.listViews[0]?.maxLimit ?? 100);

    return {
      where: and(
        eq(records.tenantId, tenantId),
        eq(records.entity, entity.name),
        eq(records.deleted, false),
      ),
      limit,
      orderBy: [desc(records.createdAt)],
    };
  }
}
