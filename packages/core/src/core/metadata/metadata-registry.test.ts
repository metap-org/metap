import { describe, expect, it } from "vitest";
import { z } from "zod";
import type { EntityDefinition } from "./entity";
import { MetadataRegistry } from "./metadata-registry";
import { MetadataValidationError } from "./metadata-compiler";

function widgetEntity(overrides: Partial<EntityDefinition> = {}): EntityDefinition {
  return {
    name: "test.widgets",
    label: "Widget",
    tableName: "records",
    schema: z.object({ name: z.string() }),
    fields: [{ name: "name", label: "Name", kind: "string" }],
    listViews: [],
    ...overrides,
  };
}

describe("MetadataRegistry.validateReferences", () => {
  it("throws when a reference field points at an unregistered entity", () => {
    const registry = new MetadataRegistry();
    registry.register(
      widgetEntity({
        fields: [
          { name: "name", label: "Name", kind: "string" },
          { name: "ownerId", label: "Owner", kind: "reference", refEntity: "test.nonexistent" },
        ],
      }),
    );

    expect(() => registry.validateReferences()).toThrow(MetadataValidationError);
  });

  it("does not throw when a reference field points at a registered entity", () => {
    const registry = new MetadataRegistry();
    registry.register(widgetEntity({ name: "test.owners" }));
    registry.register(
      widgetEntity({
        name: "test.widgets",
        fields: [
          { name: "name", label: "Name", kind: "string" },
          { name: "ownerId", label: "Owner", kind: "reference", refEntity: "test.owners" },
        ],
      }),
    );

    expect(() => registry.validateReferences()).not.toThrow();
  });

  it("does not throw when refDisplayField names a real field on the target entity", () => {
    const registry = new MetadataRegistry();
    registry.register(widgetEntity({ name: "test.owners" }));
    registry.register(
      widgetEntity({
        name: "test.widgets",
        fields: [
          { name: "name", label: "Name", kind: "string" },
          {
            name: "ownerId",
            label: "Owner",
            kind: "reference",
            refEntity: "test.owners",
            refDisplayField: "name",
          },
        ],
      }),
    );

    expect(() => registry.validateReferences()).not.toThrow();
  });

  it("throws when refDisplayField names a field that doesn't exist on the target entity", () => {
    const registry = new MetadataRegistry();
    registry.register(widgetEntity({ name: "test.owners" }));
    registry.register(
      widgetEntity({
        name: "test.widgets",
        fields: [
          { name: "name", label: "Name", kind: "string" },
          {
            name: "ownerId",
            label: "Owner",
            kind: "reference",
            refEntity: "test.owners",
            refDisplayField: "nickname",
          },
        ],
      }),
    );

    expect(() => registry.validateReferences()).toThrow(MetadataValidationError);
  });
});
