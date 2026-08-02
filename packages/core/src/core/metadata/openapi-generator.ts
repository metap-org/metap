import { z } from "zod";
import type { EntityField, FieldKind } from "./entity";
import type { EntitySummary } from "./metadata-registry";
import { EntitySummarySchema } from "./entity-wire-schema";

type JsonSchema = Record<string, unknown>;

export const FIELD_KIND_TO_JSON_SCHEMA: Record<FieldKind, JsonSchema> = {
  id: { type: "string" },
  string: { type: "string" },
  number: { type: "number" },
  boolean: { type: "boolean" },
  date: { type: "string", format: "date" },
  datetime: { type: "string", format: "date-time" },
  money: { type: "number" },
  enum: { type: "string" },
  reference: { type: "string" },
  json: {},
};

function fieldSchema(field: EntityField): JsonSchema {
  if (field.kind === "enum") {
    return { type: "string", enum: [...(field.enumValues ?? [])] };
  }
  return FIELD_KIND_TO_JSON_SCHEMA[field.kind];
}

function entitySchema(entity: EntitySummary): JsonSchema {
  const properties: Record<string, JsonSchema> = {};
  for (const field of entity.fields) {
    properties[field.name] = fieldSchema(field);
  }
  return {
    type: "object",
    properties,
    required: entity.fields.filter((f) => f.required).map((f) => f.name),
  };
}

export type OpenApiDocument = {
  openapi: "3.1.0";
  info: { title: string; version: string };
  paths: Record<string, Record<string, unknown>>;
  components: { schemas: Record<string, JsonSchema> };
};

export function generateOpenApiDocument(entities: readonly EntitySummary[]): OpenApiDocument {
  const paths: Record<string, Record<string, unknown>> = {};

  paths["/metadata/entities"] = {
    get: {
      summary: "List entity metadata",
      responses: {
        "200": {
          description: "OK",
          content: {
            "application/json": {
              schema: {
                type: "object",
                properties: {
                  data: {
                    type: "array",
                    items: { $ref: "#/components/schemas/EntitySummary" },
                  },
                },
              },
            },
          },
        },
      },
    },
  };

  paths["/metadata/entities/{entity}"] = {
    get: {
      summary: "Get one entity's metadata",
      responses: {
        "200": {
          description: "OK",
          content: {
            "application/json": {
              schema: {
                type: "object",
                properties: { data: { $ref: "#/components/schemas/EntitySummary" } },
              },
            },
          },
        },
        "404": { description: "Not found" },
      },
    },
  };

  for (const entity of entities) {
    const schema = entitySchema(entity);
    const listPath = `/api/${entity.name}`;
    const itemPath = `/api/${entity.name}/{id}`;

    paths[listPath] = {
      get: {
        summary: `List ${entity.label}`,
        responses: { "200": { description: "OK" } },
      },
      post: {
        summary: `Create ${entity.label}`,
        requestBody: {
          content: {
            "application/json": { schema: { type: "object", properties: { data: schema } } },
          },
        },
        responses: { "201": { description: "Created" } },
      },
    };

    paths[itemPath] = {
      patch: {
        summary: `Update ${entity.label}`,
        requestBody: {
          content: {
            "application/json": {
              schema: {
                type: "object",
                properties: { version: { type: "number" }, data: schema },
              },
            },
          },
        },
        responses: { "200": { description: "OK" } },
      },
    };

    if (entity.workflow) {
      paths[`/api/${entity.name}/{id}/transitions/{action}`] = {
        post: {
          summary: `Transition ${entity.label}`,
          requestBody: {
            content: {
              "application/json": {
                schema: { type: "object", properties: { version: { type: "number" } } },
              },
            },
          },
          responses: { "200": { description: "OK" } },
        },
      };
    }
  }

  return {
    openapi: "3.1.0",
    info: { title: "Metap API", version: "1.0.0" },
    paths,
    components: {
      schemas: {
        EntitySummary: z.toJSONSchema(EntitySummarySchema, { target: "draft-7" }),
      },
    },
  };
}
