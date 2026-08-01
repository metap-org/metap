import { createHash } from "node:crypto";
import type { EntityDefinition } from "./entity";

// Always available on every entity's underlying `records` row, regardless of
// entity metadata — see `QueryPlanner`'s `sortableFields`/`fieldExpression`,
// which resolve these to real DB columns independent of `entity.fields`.
const IMPLICIT_SYSTEM_FIELDS = new Set(["createdAt", "updatedAt"]);

export class MetadataValidationError extends Error {
  constructor(
    public readonly entity: string,
    public readonly issues: readonly string[],
  ) {
    super(`Invalid metadata for entity "${entity}": ${issues.join("; ")}`);
    this.name = "MetadataValidationError";
  }
}

function stableStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(",")}]`;
  }
  if (value !== null && typeof value === "object") {
    const keys = Object.keys(value).sort();
    return `{${keys
      .map(
        (key) => `${JSON.stringify(key)}:${stableStringify((value as Record<string, unknown>)[key])}`,
      )
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

export const MetadataCompiler = {
  hash(entity: EntityDefinition): string {
    const shape = {
      label: entity.label,
      fields: entity.fields,
      listViews: entity.listViews,
      workflow: entity.workflow
        ? {
            stateField: entity.workflow.stateField,
            initialState: entity.workflow.initialState,
            terminalStates: entity.workflow.terminalStates,
            transitions: entity.workflow.transitions.map((t) => ({
              action: t.action,
              from: t.from,
              to: t.to,
              label: t.label,
              // guard functions are intentionally excluded — unrepresentable, already stripped on the wire
            })),
          }
        : undefined,
    };
    return createHash("sha256").update(stableStringify(shape)).digest("hex");
  },

  validate(entity: EntityDefinition): void {
    const issues: string[] = [];
    const fieldNames = new Set<string>();

    for (const field of entity.fields) {
      if (fieldNames.has(field.name)) {
        issues.push(`duplicate field name "${field.name}"`);
      }
      fieldNames.add(field.name);

      if (field.kind === "enum" && (!field.enumValues || field.enumValues.length === 0)) {
        issues.push(`field "${field.name}" is kind "enum" but declares no enumValues`);
      }

      if (field.kind === "reference" && !field.refEntity) {
        issues.push(`field "${field.name}" is kind "reference" but declares no refEntity`);
      }
    }

    for (const listView of entity.listViews) {
      for (const fieldName of listView.fields) {
        if (!fieldNames.has(fieldName) && !IMPLICIT_SYSTEM_FIELDS.has(fieldName)) {
          issues.push(`listView "${listView.name}" references unknown field "${fieldName}"`);
        }
      }
      for (const fieldName of listView.filters) {
        if (!fieldNames.has(fieldName) && !IMPLICIT_SYSTEM_FIELDS.has(fieldName)) {
          issues.push(`listView "${listView.name}" filters on unknown field "${fieldName}"`);
        }
      }
      if (listView.defaultSort) {
        const sortField = listView.defaultSort.replace(/^-/, "");
        if (!fieldNames.has(sortField) && !IMPLICIT_SYSTEM_FIELDS.has(sortField)) {
          issues.push(`listView "${listView.name}" defaultSort references unknown field "${sortField}"`);
        }
      }
    }

    if (entity.workflow) {
      const { stateField, initialState, terminalStates, transitions } = entity.workflow;

      if (!fieldNames.has(stateField)) {
        issues.push(`workflow.stateField "${stateField}" is not a declared field`);
      }
      if (!initialState) {
        issues.push("workflow.initialState must be a non-empty string");
      }
      for (const state of terminalStates) {
        if (!state) {
          issues.push("workflow.terminalStates contains an empty string");
        }
      }

      const seenTransitionKeys = new Set<string>();
      for (const transition of transitions) {
        if (!transition.from || !transition.to || !transition.action) {
          issues.push(
            `workflow transition is missing from/to/action: ${JSON.stringify({
              from: transition.from,
              to: transition.to,
              action: transition.action,
            })}`,
          );
          continue;
        }
        const key = `${transition.from}::${transition.action}`;
        if (seenTransitionKeys.has(key)) {
          issues.push(
            `duplicate transition action "${transition.action}" from state "${transition.from}"`,
          );
        }
        seenTransitionKeys.add(key);
      }
    }

    if (issues.length > 0) {
      throw new MetadataValidationError(entity.name, issues);
    }
  },
};
