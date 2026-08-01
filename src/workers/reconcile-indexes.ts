import { createContainer } from "../core/container";
import { registerEntities } from "../modules/registry";
import { loadConfig } from "../server/config";

const config = loadConfig();
const container = createContainer(config);
registerEntities(container.metadata);

const log = {
  info: (obj: unknown, msg: string) => console.log(msg, obj),
  warn: (obj: unknown, msg: string) => console.warn(msg, obj),
};

try {
  await container.indexReconciler.reconcile(container.metadata.listEntities(), log);
} finally {
  await container.close();
}
