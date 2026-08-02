import type { AppConfig } from "../server/config";
import { createJwtVerifier } from "./auth/jwt-verifier";
import { RoleAssignmentService } from "./auth/role-assignment-service";
import { createDatabase } from "../infra/db/client";
import { createRabbitPublisher } from "../infra/messaging/rabbitmq";
import { CrudService } from "./crud/crud-service";
import { HealthService } from "./health/health-service";
import { IndexReconciler } from "./metadata/index-reconciler";
import { MetadataDriftService } from "./metadata/metadata-drift";
import { MetadataRegistry } from "./metadata/metadata-registry";
import { OutboxService } from "./outbox/outbox-service";
import { PermissionService } from "./permission/permission-service";
import { PostgresPolicyStore } from "./permission/policy-store";
import { QueryPlanner } from "./query/query-planner";
import { WorkflowEngine } from "./workflow/workflow-engine";

export function createContainer(config: AppConfig) {
  const db = createDatabase(config.databaseUrl);
  // Decoupled from `db` on purpose: publishPending() reads/writes outbox_events
  // through this connection, not through enqueue()'s caller-supplied transaction
  // (which already ties an outbox write to wherever the business write happens).
  // Not set -> reuses `db`, today's behavior. Set -> must point at whatever
  // database outbox_events actually lives in for this deployment.
  const outboxDb = config.outboxDatabaseUrl ? createDatabase(config.outboxDatabaseUrl) : db;
  const auth = createJwtVerifier(config.authJwtPublicKeyPath);
  const roleAssignments = new RoleAssignmentService(db);
  const rabbit = createRabbitPublisher(config.rabbitmqUrl);

  // Entity registration is an application-layer concern, not core's — call
  // registerEntities(container.metadata, ...) after createContainer() returns.
  // See src/modules/registry.ts.
  const metadata = new MetadataRegistry();

  const policyStore = new PostgresPolicyStore(db);
  const permissions = new PermissionService(policyStore);
  const queryPlanner = new QueryPlanner(metadata, permissions);
  const outbox = new OutboxService(outboxDb, rabbit);
  const workflow = new WorkflowEngine(outbox);
  const crud = new CrudService(db, metadata, queryPlanner, permissions, workflow, outbox);
  const health = new HealthService(db);
  const metadataDrift = new MetadataDriftService(db);
  const indexReconciler = new IndexReconciler(db);

  return {
    db,
    outboxDb,
    auth,
    roleAssignments,
    rabbit,
    metadata,
    permissions,
    queryPlanner,
    outbox,
    workflow,
    crud,
    health,
    metadataDrift,
    indexReconciler,
    async close() {
      await rabbit.close();
      if (outboxDb !== db) {
        await outboxDb.close();
      }
      await db.close();
    },
  };
}

export type AppContainer = ReturnType<typeof createContainer>;
