import { z } from "zod";
import { describe, expect, it } from "vitest";
import type { EntityDefinition } from "../metadata/entity";
import { MetadataRegistry } from "../metadata/metadata-registry";
import { PermissionService } from "./permission-service";
import type { RequestContext } from "./permission-service";

const TestEntitySchema = z.object({ name: z.string() });

const restrictedEntity: EntityDefinition<typeof TestEntitySchema> = {
  name: "test.restricted",
  label: "Restricted Test Entity",
  tableName: "records",
  schema: TestEntitySchema,
  fields: [{ name: "name", label: "Name", kind: "string" }],
  listViews: [
    {
      name: "default",
      label: "Default",
      fields: ["name"],
      filters: [],
      maxLimit: 100,
    },
  ],
  permissions: {
    read: ["viewer", "editor"],
    create: ["editor"],
    update: ["editor"],
  },
};

const openEntity: EntityDefinition<typeof TestEntitySchema> = {
  name: "test.open",
  label: "Open Test Entity",
  tableName: "records",
  schema: TestEntitySchema,
  fields: [{ name: "name", label: "Name", kind: "string" }],
  listViews: [
    {
      name: "default",
      label: "Default",
      fields: ["name"],
      filters: [],
      maxLimit: 100,
    },
  ],
};

function buildService() {
  const metadata = new MetadataRegistry();
  metadata.register(restrictedEntity);
  metadata.register(openEntity);
  return new PermissionService(metadata);
}

function contextWithRoles(roles: string[]): RequestContext {
  return { tenantId: "00000000-0000-0000-0000-000000000001", roles };
}

describe("PermissionService", () => {
  it("allows admin regardless of the entity's declared permissions", () => {
    const permissions = buildService();
    const decision = permissions.canCreateEntity(contextWithRoles(["admin"]), "test.restricted");
    expect(decision.allowed).toBe(true);
  });

  it("allows any role when the entity declares no permissions", () => {
    const permissions = buildService();
    const decision = permissions.canReadEntity(
      contextWithRoles(["nobody-in-particular"]),
      "test.open",
    );
    expect(decision.allowed).toBe(true);
  });

  it("allows a role that is in the entity's allowed list", () => {
    const permissions = buildService();
    const decision = permissions.canReadEntity(contextWithRoles(["viewer"]), "test.restricted");
    expect(decision.allowed).toBe(true);
  });

  it("denies a role that is not in the entity's allowed list", () => {
    const permissions = buildService();
    const decision = permissions.canCreateEntity(contextWithRoles(["viewer"]), "test.restricted");
    expect(decision.allowed).toBe(false);
    expect(decision.reason).toBe("forbidden");
  });
});
