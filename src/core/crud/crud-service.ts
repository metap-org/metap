import { and, eq, sql } from "drizzle-orm";
import type { z } from "zod";
import type { Database } from "../../infra/db/client";
import { records } from "../../infra/db/schema";
import type { MetadataRegistry } from "../metadata/metadata-registry";
import type { OutboxService } from "../outbox/outbox-service";
import type { PermissionService, RequestContext } from "../permission/permission-service";
import type { ListInput, QueryPlanner } from "../query/query-planner";
import type { WorkflowEngine } from "../workflow/workflow-engine";
import type { ServiceResult } from "./result";

type RecordDto = {
  id: string;
  entity: string;
  code: string | null;
  status: string | null;
  data: unknown;
  version: number;
  createdAt: Date;
  updatedAt: Date;
};

export class CrudService {
  constructor(
    private readonly db: Database,
    private readonly metadata: MetadataRegistry,
    private readonly queryPlanner: QueryPlanner,
    private readonly permissions: PermissionService,
    private readonly workflow: WorkflowEngine,
    private readonly outbox: OutboxService,
  ) {}

  async list(
    entityName: string,
    input: ListInput,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto[]>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canReadEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const recordPolicies = snapshot.getRecordPolicies("read");

    const plan = this.queryPlanner.planList(entity.name, input, context, recordPolicies);
    const rows = await this.db.client
      .select()
      .from(records)
      .where(plan.where)
      .orderBy(...plan.orderBy)
      .limit(plan.limit);

    const data = rows.map((row) => ({
      ...row,
      data: snapshot.filterReadableFields(context, row.data as Record<string, unknown>),
    }));

    return {
      ok: true,
      data,
      page: {
        limit: plan.limit,
      },
    };
  }

  async create(
    entityName: string,
    rawData: Record<string, unknown>,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canCreateEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), undefined);

    if (!writeDecision.allowed) {
      return { ok: false, status: 403, error: writeDecision.reason ?? "forbidden" };
    }

    const parsed = entity.schema.safeParse(rawData);

    if (!parsed.success) {
      return { ok: false, status: 400, error: "validation_failed" };
    }

    const data = parsed.data as Record<string, unknown>;
    const status = this.workflow.getInitialStatus(entity, data);

    const outcome = await this.db.client.transaction(async (tx) => {
      const inserted = await tx
        .insert(records)
        .values({
          tenantId: context.tenantId,
          entity: entity.name,
          code: typeof data.code === "string" ? data.code : null,
          status,
          data,
          createdBy: context.userId,
          updatedBy: context.userId,
        })
        .returning();

      const record = inserted[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.emitCreated(tx, entity, record.id, data);

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 500, error: "insert_failed" };
    }

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: snapshot.filterReadableFields(context, outcome.record.data as Record<string, unknown>),
      },
    };
  }

  async update(
    entityName: string,
    id: string,
    expectedVersion: number,
    rawData: Record<string, unknown>,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canUpdateEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const existingRows = await this.db.client
      .select()
      .from(records)
      .where(
        and(
          eq(records.id, id),
          eq(records.tenantId, context.tenantId),
          eq(records.entity, entity.name),
          eq(records.deleted, false),
        ),
      );

    const existing = existingRows[0];

    if (!existing) {
      return { ok: false, status: 404, error: "record_not_found" };
    }

    const existingData = existing.data as Record<string, unknown>;

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const recordDecision = snapshot.canUpdateRecordCondition(context, existingData);

    if (!recordDecision.allowed) {
      return { ok: false, status: 403, error: recordDecision.reason ?? "forbidden" };
    }

    const writeDecision = snapshot.assertWritableFields(context, Object.keys(rawData), existingData);

    if (!writeDecision.allowed) {
      return { ok: false, status: 403, error: writeDecision.reason ?? "forbidden" };
    }

    const mergedData: Record<string, unknown> = { ...existingData, ...rawData };

    if (entity.workflow) {
      // The state field can never change through this path — only `create` and
      // `transition` are allowed to move it — so it's always reset to its existing value.
      mergedData[entity.workflow.stateField] = existingData[entity.workflow.stateField];
    }

    const parsed = entity.schema.safeParse(mergedData);

    if (!parsed.success) {
      return { ok: false, status: 400, error: "validation_failed" };
    }

    const data = parsed.data as Record<string, unknown>;
    const code = typeof data.code === "string" ? data.code : null;

    const outcome = await this.db.client.transaction(async (tx) => {
      const updatedRows = await tx
        .update(records)
        .set({
          data,
          code,
          version: sql`${records.version} + 1`,
          updatedAt: new Date(),
          updatedBy: context.userId,
        })
        .where(
          and(
            eq(records.id, id),
            eq(records.tenantId, context.tenantId),
            eq(records.entity, entity.name),
            eq(records.version, expectedVersion),
            eq(records.deleted, false),
          ),
        )
        .returning();

      const record = updatedRows[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.emitUpdated(tx, entity, record.id, data, record.version);

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: snapshot.filterReadableFields(context, outcome.record.data as Record<string, unknown>),
      },
    };
  }

  async transition(
    entityName: string,
    id: string,
    action: string,
    expectedVersion: number,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canUpdateEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const existingRows = await this.db.client
      .select()
      .from(records)
      .where(
        and(
          eq(records.id, id),
          eq(records.tenantId, context.tenantId),
          eq(records.entity, entity.name),
          eq(records.deleted, false),
        ),
      );

    const existing = existingRows[0];

    if (!existing) {
      return { ok: false, status: 404, error: "record_not_found" };
    }

    const existingData = existing.data as Record<string, unknown>;

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const recordDecision = snapshot.canUpdateRecordCondition(context, existingData);

    if (!recordDecision.allowed) {
      return { ok: false, status: 403, error: recordDecision.reason ?? "forbidden" };
    }

    if (!entity.workflow) {
      return { ok: false, status: 400, error: "no_workflow" };
    }

    const fromState = existingData[entity.workflow.stateField];

    if (typeof fromState !== "string") {
      return { ok: false, status: 409, error: "invalid_transition" };
    }

    const transition = this.workflow.findTransition(entity, action, fromState);

    if (!transition) {
      return { ok: false, status: 409, error: "invalid_transition" };
    }

    const guardResult = this.workflow.runGuard(transition, existingData, context);

    if (guardResult !== true) {
      return { ok: false, status: 422, error: "guard_failed", message: guardResult };
    }

    const nextData: Record<string, unknown> = {
      ...existingData,
      [entity.workflow.stateField]: transition.to,
    };

    const outcome = await this.db.client.transaction(async (tx) => {
      const updatedRows = await tx
        .update(records)
        .set({
          data: nextData,
          status: transition.to,
          version: sql`${records.version} + 1`,
          updatedAt: new Date(),
          updatedBy: context.userId,
        })
        .where(
          and(
            eq(records.id, id),
            eq(records.tenantId, context.tenantId),
            eq(records.entity, entity.name),
            eq(records.version, expectedVersion),
            eq(records.deleted, false),
          ),
        )
        .returning();

      const record = updatedRows[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.recordEvent(
        tx,
        entity,
        record.id,
        action,
        fromState,
        transition.to,
        context,
      );
      await this.workflow.emitTransitioned(
        tx,
        entity,
        record.id,
        action,
        fromState,
        transition.to,
        context.userId,
      );

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    return {
      ok: true,
      data: {
        ...outcome.record,
        data: snapshot.filterReadableFields(context, outcome.record.data as Record<string, unknown>),
      },
    };
  }

  async flushOutbox() {
    await this.outbox.publishPending();
  }
}
