import { assertCoreSchemaPresent, buildApp, createDatabase, loadConfig } from "@metap/core";
import { entities } from "./modules/registry";

const config = loadConfig();

const schemaCheckDb = createDatabase(config.databaseUrl);
try {
  await assertCoreSchemaPresent(schemaCheckDb);
} finally {
  await schemaCheckDb.close();
}

const app = await buildApp(config, entities);

try {
  await app.listen({ host: config.host, port: config.port });
} catch (error) {
  app.log.error(error);
  process.exit(1);
}
