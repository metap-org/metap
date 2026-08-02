import { buildApp, loadConfig } from "@metap/core";
import { entities } from "./modules/registry";

const config = loadConfig();
const app = await buildApp(config, entities);

try {
  await app.listen({ host: config.host, port: config.port });
} catch (error) {
  app.log.error(error);
  process.exit(1);
}
