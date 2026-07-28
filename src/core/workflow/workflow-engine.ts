import type { EntityDefinition } from "../metadata/entity";
import type { OutboxService } from "../outbox/outbox-service";

export class WorkflowEngine {
  constructor(private readonly outbox: OutboxService) {}

  getInitialStatus(entity: EntityDefinition, data: Record<string, unknown>) {
    if (!entity.workflow) {
      return undefined;
    }

    const explicitStatus = data[entity.workflow.stateField];
    return typeof explicitStatus === "string" ? explicitStatus : entity.workflow.initialState;
  }

  async emitCreated(entity: EntityDefinition, recordId: string, data: Record<string, unknown>) {
    await this.outbox.enqueue({
      topic: `${entity.name}.record.created`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: {
        recordId,
        data,
      },
    });
  }
}
