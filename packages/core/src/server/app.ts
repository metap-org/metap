import cors from "@fastify/cors";
import helmet from "@fastify/helmet";
import rateLimit from "@fastify/rate-limit";
import Fastify from "fastify";
import type { AppConfig } from "./config";
import { createContainer } from "../core/container";
import type { EntityDefinition } from "../core/metadata/entity";
import { registerErrorHandler } from "./error-handler";
import { registerAuthHook } from "./plugins/auth-hook";
import { registerRequestContextHooks } from "./plugins/request-id";
import { registerHealthRoutes } from "./routes/health";
import { registerMetadataRoutes, registerOpenApiRoute } from "./routes/metadata";
import { registerAdminRoutes } from "./routes/admin";
import { registerRecordRoutes } from "./routes/records";

export async function buildApp(config: AppConfig, entities: readonly EntityDefinition[]) {
  const app = Fastify({
    logger: {
      level: config.nodeEnv === "production" ? "info" : "debug",
    },
    ajv: {
      customOptions: {
        removeAdditional: true,
        coerceTypes: true,
        allErrors: true,
      },
    },
  });

  await app.register(helmet);
  await app.register(cors, {
    origin: config.corsOrigins,
    credentials: true,
  });
  await app.register(rateLimit, {
    max: 300,
    timeWindow: "1 minute",
  });

  registerRequestContextHooks(app);
  registerErrorHandler(app);

  const container = createContainer(config);
  for (const entity of entities) {
    container.metadata.register(entity);
  }
  container.metadata.validateReferences();
  await container.metadataDrift.check(container.metadata.listEntities(), app.log);
  await container.indexReconciler.reconcile(container.metadata.listEntities(), app.log);

  registerHealthRoutes(app, container);
  registerOpenApiRoute(app, container);

  await app.register((protectedApp) => {
    registerAuthHook(protectedApp, container.auth, container.roleAssignments);
    registerMetadataRoutes(protectedApp, container);
    registerRecordRoutes(protectedApp, container);
    registerAdminRoutes(protectedApp, container);
  });

  app.addHook("onClose", async () => {
    await container.close();
  });

  return app;
}
