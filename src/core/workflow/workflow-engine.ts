import type { DbExecutor } from "../../infra/db/client";
import { workflowEvents } from "../../infra/db/schema";
import type { EntityDefinition, WorkflowTransition } from "../metadata/entity";
import type { OutboxService } from "../outbox/outbox-service";
import type { RequestContext } from "../permission/permission-service";

export class WorkflowEngine {
  constructor(private readonly outbox: OutboxService) {}

  getInitialStatus(entity: EntityDefinition, data: Record<string, unknown>) {
    if (!entity.workflow) {
      return undefined;
    }

    const explicitStatus = data[entity.workflow.stateField];
    return typeof explicitStatus === "string" ? explicitStatus : entity.workflow.initialState;
  }

  findTransition(
    entity: EntityDefinition,
    action: string,
    fromState: string,
  ): WorkflowTransition | undefined {
    return entity.workflow?.transitions.find((t) => t.action === action && t.from === fromState);
  }

  runGuard(
    transition: WorkflowTransition,
    data: Record<string, unknown>,
    context: RequestContext,
  ): true | string {
    if (!transition.guard) {
      return true;
    }

    return transition.guard(data, context);
  }

  async recordEvent(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    action: string,
    fromState: string,
    toState: string,
    context: RequestContext,
  ) {
    await executor.insert(workflowEvents).values({
      tenantId: context.tenantId,
      entity: entity.name,
      recordId,
      action,
      fromState,
      toState,
      actor: context.userId,
    });
  }

  async emitTransitioned(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    action: string,
    fromState: string,
    toState: string,
    actor: string | undefined,
  ) {
    await this.outbox.enqueue(executor, {
      topic: `${entity.name}.workflow.transitioned`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: { recordId, action, from: fromState, to: toState, actor },
    });
  }

  async emitCreated(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    data: Record<string, unknown>,
  ) {
    await this.outbox.enqueue(executor, {
      topic: `${entity.name}.record.created`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: {
        recordId,
        data,
      },
    });
  }

  async emitUpdated(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    data: Record<string, unknown>,
    version: number,
  ) {
    await this.outbox.enqueue(executor, {
      topic: `${entity.name}.record.updated`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: {
        recordId,
        data,
        version,
      },
    });
  }
}
