import type { AppConfig } from "../server/config";
import { createJwtVerifier } from "./auth/jwt-verifier";
import { createDatabase } from "../infra/db/client";
import { createRabbitPublisher } from "../infra/messaging/rabbitmq";
import { customerEntity } from "../modules/crm/customer.entity";
import { CrudService } from "./crud/crud-service";
import { HealthService } from "./health/health-service";
import { MetadataRegistry } from "./metadata/metadata-registry";
import { OutboxService } from "./outbox/outbox-service";
import { PermissionService } from "./permission/permission-service";
import { QueryPlanner } from "./query/query-planner";
import { WorkflowEngine } from "./workflow/workflow-engine";

export function createContainer(config: AppConfig) {
  const db = createDatabase(config.databaseUrl);
  const auth = createJwtVerifier(config.authJwtPublicKeyPath);
  const rabbit = createRabbitPublisher(config.rabbitmqUrl);

  const metadata = new MetadataRegistry();
  metadata.register(customerEntity);

  const permissions = new PermissionService();
  const queryPlanner = new QueryPlanner(metadata, permissions);
  const outbox = new OutboxService(db, rabbit);
  const workflow = new WorkflowEngine(outbox);
  const crud = new CrudService(db, metadata, queryPlanner, permissions, workflow, outbox);
  const health = new HealthService(db);

  return {
    db,
    auth,
    rabbit,
    metadata,
    permissions,
    queryPlanner,
    outbox,
    workflow,
    crud,
    health,
    async close() {
      await rabbit.close();
      await db.close();
    },
  };
}

export type AppContainer = ReturnType<typeof createContainer>;
