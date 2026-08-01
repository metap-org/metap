import { describe, expect, it } from "vitest";
import { z } from "zod";
import type { EntityDefinition } from "./entity";
import { MetadataCompiler, MetadataValidationError } from "./metadata-compiler";

function baseEntity(overrides: Partial<EntityDefinition> = {}): EntityDefinition {
  return {
    name: "test.widgets",
    label: "Widget",
    tableName: "records",
    schema: z.object({ name: z.string() }),
    fields: [
      { name: "name", label: "Name", kind: "string" },
      { name: "status", label: "Status", kind: "enum", enumValues: ["draft", "active"] },
    ],
    listViews: [
      {
        name: "default",
        label: "Default",
        fields: ["name", "status"],
        filters: ["name", "status"],
        defaultSort: "-name",
        maxLimit: 50,
      },
    ],
    ...overrides,
  };
}

describe("MetadataCompiler.hash", () => {
  it("is deterministic for the same entity shape", () => {
    const entity = baseEntity();
    expect(MetadataCompiler.hash(entity)).toBe(MetadataCompiler.hash(baseEntity()));
  });

  it("differs when a field's label changes", () => {
    const original = MetadataCompiler.hash(baseEntity());
    const changed = MetadataCompiler.hash(
      baseEntity({
        fields: [
          { name: "name", label: "Full Name", kind: "string" },
          { name: "status", label: "Status", kind: "enum", enumValues: ["draft", "active"] },
        ],
      }),
    );
    expect(changed).not.toBe(original);
  });

  it("is unaffected by workflow transition guard presence", () => {
    const withoutGuard = MetadataCompiler.hash(
      baseEntity({
        workflow: {
          stateField: "status",
          initialState: "draft",
          terminalStates: ["active"],
          transitions: [{ action: "activate", from: "draft", to: "active", label: "Activate" }],
        },
      }),
    );
    const withGuard = MetadataCompiler.hash(
      baseEntity({
        workflow: {
          stateField: "status",
          initialState: "draft",
          terminalStates: ["active"],
          transitions: [
            {
              action: "activate",
              from: "draft",
              to: "active",
              label: "Activate",
              guard: () => true,
            },
          ],
        },
      }),
    );
    expect(withGuard).toBe(withoutGuard);
  });
});

describe("MetadataCompiler.validate", () => {
  it("passes on a valid entity", () => {
    expect(() => MetadataCompiler.validate(baseEntity())).not.toThrow();
  });

  it("accepts createdAt/updatedAt in defaultSort and filters as implicit system fields", () => {
    const entity = baseEntity({
      listViews: [
        {
          name: "default",
          label: "Default",
          fields: ["name", "createdAt"],
          filters: ["updatedAt"],
          defaultSort: "-createdAt",
          maxLimit: 50,
        },
      ],
    });

    expect(() => MetadataCompiler.validate(entity)).not.toThrow();
  });

  it("throws when a listView field references an unknown field", () => {
    const entity = baseEntity({
      listViews: [
        {
          name: "default",
          label: "Default",
          fields: ["name", "nonexistent"],
          filters: [],
          maxLimit: 50,
        },
      ],
    });

    expect(() => MetadataCompiler.validate(entity)).toThrow(MetadataValidationError);
    try {
      MetadataCompiler.validate(entity);
    } catch (error) {
      expect((error as MetadataValidationError).issues.some((i) => i.includes("nonexistent"))).toBe(
        true,
      );
    }
  });

  it("throws when an enum field declares no enumValues", () => {
    const entity = baseEntity({
      fields: [{ name: "name", label: "Name", kind: "string" }, { name: "status", label: "Status", kind: "enum" }],
    });

    expect(() => MetadataCompiler.validate(entity)).toThrow(/enumValues/);
  });

  it("throws when workflow.stateField is not a declared field", () => {
    const entity = baseEntity({
      workflow: {
        stateField: "nonexistent",
        initialState: "draft",
        terminalStates: [],
        transitions: [],
      },
    });

    expect(() => MetadataCompiler.validate(entity)).toThrow(/stateField/);
  });

  it("throws when two transitions share the same (from, action) pair", () => {
    const entity = baseEntity({
      workflow: {
        stateField: "status",
        initialState: "draft",
        terminalStates: ["active"],
        transitions: [
          { action: "activate", from: "draft", to: "active", label: "Activate" },
          { action: "activate", from: "draft", to: "active", label: "Activate again" },
        ],
      },
    });

    expect(() => MetadataCompiler.validate(entity)).toThrow(/duplicate transition/);
  });
});
