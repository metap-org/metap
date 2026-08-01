import type { SQL } from "drizzle-orm";
import { and, asc, desc, eq, sql } from "drizzle-orm";
import { records } from "../../infra/db/schema";
import type { MetadataRegistry } from "../metadata/metadata-registry";
import type { PermissionService, PolicyRow, RequestContext } from "../permission/permission-service";
import { recordPolicyWhereClause } from "./condition-to-sql";

export type ListInput = {
  limit: number;
  sort?: string;
  filters?: Record<string, string>;
};

export type PlannedListQuery = {
  where: SQL | undefined;
  limit: number;
  orderBy: SQL[];
};

type ResolvedSort = { field: string; descending: boolean };

function parseSort(
  candidate: string | undefined,
  sortableFields: ReadonlySet<string>,
): ResolvedSort | undefined {
  if (!candidate) {
    return undefined;
  }

  const descending = candidate.startsWith("-");
  const field = descending ? candidate.slice(1) : candidate;

  return sortableFields.has(field) ? { field, descending } : undefined;
}

function fieldExpression(fieldName: string) {
  if (fieldName === "createdAt") {
    return records.createdAt;
  }
  if (fieldName === "updatedAt") {
    return records.updatedAt;
  }
  return sql`jsonb_extract_path_text(${records.data}, ${fieldName})`;
}

export class QueryPlanner {
  constructor(
    private readonly metadata: MetadataRegistry,
    private readonly permissions: PermissionService,
  ) {}

  planList(
    entityName: string,
    input: ListInput,
    context: Partial<RequestContext>,
    recordReadPolicies: PolicyRow[] = [],
  ): PlannedListQuery {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      throw new Error(`Entity not found: ${entityName}`);
    }

    const tenantId = this.permissions.scopedTenant(context);
    const listView = entity.listViews[0];
    const limit = Math.min(input.limit, listView?.maxLimit ?? 100);

    const conditions: SQL[] = [
      eq(records.tenantId, tenantId),
      eq(records.entity, entity.name),
      eq(records.deleted, false),
    ];

    const recordCondition = recordPolicyWhereClause(recordReadPolicies, context as RequestContext);
    if (recordCondition) {
      conditions.push(recordCondition);
    }

    const allowedFilterFields = new Set(listView?.filters ?? []);
    const fieldsByName = new Map(entity.fields.map((field) => [field.name, field]));

    for (const [field, value] of Object.entries(input.filters ?? {})) {
      if (!allowedFilterFields.has(field)) {
        continue;
      }

      const fieldExpr = fieldExpression(field);
      const fieldDef = fieldsByName.get(field);

      if (fieldDef?.searchable) {
        const escapedValue = value.replace(/[\\%_]/g, "\\$&");
        conditions.push(sql`${fieldExpr} ILIKE ${`%${escapedValue}%`}`);
      } else {
        conditions.push(sql`${fieldExpr} = ${value}`);
      }
    }

    const sortableFields = new Set<string>([
      ...entity.fields.filter((field) => field.sortable).map((field) => field.name),
      "createdAt",
      "updatedAt",
    ]);

    const resolvedSort = parseSort(input.sort, sortableFields) ??
      parseSort(listView?.defaultSort, sortableFields) ?? { field: "createdAt", descending: true };

    const sortExpr = fieldExpression(resolvedSort.field);

    return {
      where: and(...conditions),
      limit,
      orderBy: [resolvedSort.descending ? desc(sortExpr) : asc(sortExpr), asc(records.id)],
    };
  }
}
