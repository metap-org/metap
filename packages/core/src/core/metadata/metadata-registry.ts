import type { EntityDefinition, EntityField, EntityListView, EntityWorkflow } from "./entity";
import { MetadataCompiler, MetadataValidationError } from "./metadata-compiler";

export type EntitySummary = {
  name: string;
  label: string;
  fields: readonly EntityField[];
  listViews: readonly EntityListView[];
  workflow?: EntityWorkflow | undefined;
  version: string;
};

export class MetadataRegistry {
  private readonly entities = new Map<string, EntityDefinition>();

  register(entity: EntityDefinition) {
    if (this.entities.has(entity.name)) {
      throw new Error(`Entity already registered: ${entity.name}`);
    }

    MetadataCompiler.validate(entity);
    this.entities.set(entity.name, entity);
  }

  getEntity(name: string) {
    return this.entities.get(name);
  }

  validateReferences(): void {
    for (const entity of this.entities.values()) {
      const issues: string[] = [];
      for (const field of entity.fields) {
        if (field.kind === "reference" && field.refEntity && !this.entities.has(field.refEntity)) {
          issues.push(`field "${field.name}" references unknown entity "${field.refEntity}"`);
        }
        if (field.kind === "reference" && field.refEntity && field.refDisplayField) {
          const target = this.entities.get(field.refEntity);
          if (target && !target.fields.some((f) => f.name === field.refDisplayField)) {
            issues.push(
              `field "${field.name}" has refDisplayField "${field.refDisplayField}" which does not exist on "${field.refEntity}"`,
            );
          }
        }
      }
      if (issues.length > 0) {
        throw new MetadataValidationError(entity.name, issues);
      }
    }
  }

  getEntityMetadata(name: string) {
    const entity = this.entities.get(name);
    return entity ? this.toMetadata(entity) : undefined;
  }

  listEntities(): EntitySummary[] {
    return [...this.entities.values()].map((entity) => this.toMetadata(entity));
  }

  private toMetadata(entity: EntityDefinition): EntitySummary {
    return {
      name: entity.name,
      label: entity.label,
      fields: entity.fields,
      listViews: entity.listViews,
      workflow: entity.workflow,
      version: MetadataCompiler.hash(entity),
    };
  }
}
