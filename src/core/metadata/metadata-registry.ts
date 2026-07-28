import type { EntityDefinition } from "./entity";

export class MetadataRegistry {
  private readonly entities = new Map<string, EntityDefinition>();

  register(entity: EntityDefinition) {
    if (this.entities.has(entity.name)) {
      throw new Error(`Entity already registered: ${entity.name}`);
    }

    this.entities.set(entity.name, entity);
  }

  getEntity(name: string) {
    return this.entities.get(name);
  }

  listEntities() {
    return [...this.entities.values()].map((entity) => ({
      name: entity.name,
      label: entity.label,
      fields: entity.fields,
      listViews: entity.listViews,
      workflow: entity.workflow,
    }));
  }
}
