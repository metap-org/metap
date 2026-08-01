import type { EntityDefinition } from "../core/metadata/entity";
import type { MetadataRegistry } from "../core/metadata/metadata-registry";
import { customerEntity } from "./crm/customer.entity";

// The set of entity modules for this deployment. Core stays entity-agnostic;
// this is the one place that knows which business modules are wired in.
export const entities: readonly EntityDefinition[] = [customerEntity];

export function registerEntities(
  metadata: MetadataRegistry,
  entitiesToRegister: readonly EntityDefinition[] = entities,
): void {
  for (const entity of entitiesToRegister) {
    metadata.register(entity);
  }
  metadata.validateReferences();
}
