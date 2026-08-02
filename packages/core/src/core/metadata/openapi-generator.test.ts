import { describe, expect, it } from "vitest";
import type { EntitySummary } from "./metadata-registry";
import { generateOpenApiDocument } from "./openapi-generator";

const customer: EntitySummary = {
  name: "test.widgets",
  label: "Customer",
  version: "hash",
  fields: [
    { name: "name", label: "Name", kind: "string", required: true },
    { name: "status", label: "Status", kind: "enum", enumValues: ["draft", "active", "blocked"] },
  ],
  listViews: [],
  workflow: {
    stateField: "status",
    initialState: "draft",
    terminalStates: ["blocked"],
    transitions: [{ action: "activate", from: "draft", to: "active", label: "Activate" }],
  },
};

const noWorkflowEntity: EntitySummary = {
  name: "test.plain",
  label: "Plain",
  version: "hash",
  fields: [{ name: "name", label: "Name", kind: "string" }],
  listViews: [],
};

describe("generateOpenApiDocument", () => {
  it("generates a document with openapi 3.1.0 and one path group per entity", () => {
    const doc = generateOpenApiDocument([customer]);
    expect(doc.openapi).toBe("3.1.0");
    expect(doc.paths["/api/test.widgets"]).toBeDefined();
    expect(doc.paths["/api/test.widgets/{id}"]).toBeDefined();
  });

  it("includes the exact enum values for an enum field", () => {
    const doc = generateOpenApiDocument([customer]);
    const createOp = doc.paths["/api/test.widgets"]?.post as {
      requestBody: {
        content: {
          "application/json": {
            schema: { properties: { data: { properties: Record<string, { enum?: string[] }> } } };
          };
        };
      };
    };
    const statusSchema =
      createOp.requestBody.content["application/json"].schema.properties.data.properties.status;
    expect(statusSchema?.enum).toEqual(["draft", "active", "blocked"]);
  });

  it("does not generate a transitions path for an entity with no workflow", () => {
    const doc = generateOpenApiDocument([noWorkflowEntity]);
    expect(doc.paths["/api/test.plain/{id}/transitions/{action}"]).toBeUndefined();
  });

  it("generates a transitions path for an entity with a workflow", () => {
    const doc = generateOpenApiDocument([customer]);
    expect(doc.paths["/api/test.widgets/{id}/transitions/{action}"]).toBeDefined();
  });

  it("registers a self-contained EntitySummary component schema for the meta-model", () => {
    const doc = generateOpenApiDocument([customer]);
    const entitySummarySchema = doc.components?.schemas?.EntitySummary as
      { type: string; properties: Record<string, unknown> } | undefined;

    expect(entitySummarySchema).toBeDefined();
    expect(entitySummarySchema?.type).toBe("object");
    expect(entitySummarySchema?.properties.name).toBeDefined();
    expect(entitySummarySchema?.properties.fields).toBeDefined();
    expect(entitySummarySchema?.properties.workflow).toBeDefined();
    // guard is a server-side function, never sent over the wire — must not
    // leak into the generated schema for workflow transitions.
    expect(JSON.stringify(entitySummarySchema)).not.toContain("guard");
  });

  it("documents GET /metadata/entities and GET /metadata/entities/{entity}, referencing EntitySummary", () => {
    const doc = generateOpenApiDocument([customer]);

    const listOp = doc.paths["/metadata/entities"]?.get as
      { responses: Record<string, unknown> } | undefined;
    expect(listOp).toBeDefined();

    const itemOp = doc.paths["/metadata/entities/{entity}"]?.get as
      { responses: Record<string, unknown> } | undefined;
    expect(itemOp).toBeDefined();

    expect(JSON.stringify(doc.paths["/metadata/entities"])).toContain(
      "#/components/schemas/EntitySummary",
    );
    expect(JSON.stringify(doc.paths["/metadata/entities/{entity}"])).toContain(
      "#/components/schemas/EntitySummary",
    );
  });
});
