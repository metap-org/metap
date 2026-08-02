import { createContainer, loadConfig } from "@metap/core";
import { registerEntities } from "../modules/registry";

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
