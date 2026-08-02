import { z } from "zod";
import type { EntityDefinition } from "../metadata/entity";

// packages/core is entity-agnostic — real business entities (like crm.customers)
// live in apps/<module>. This fixture exists only so packages/core's own test
// suite (CrudService, QueryPlanner, buildApp, ...) has a concrete entity to
// exercise, without depending on any specific business module.
const TestWidgetSchema = z.object({
  code: z.string().min(1).max(80),
  name: z.string().min(1).max(255),
  phone: z.string().max(40).optional(),
  email: z.string().email().optional(),
  status: z.enum(["draft", "active", "blocked"]).default("draft"),
});

export const testWidgetEntity: EntityDefinition<typeof TestWidgetSchema> = {
  name: "test.widgets",
  label: "Widget",
  tableName: "records",
  schema: TestWidgetSchema,
  fields: [
    {
      name: "code",
      label: "Code",
      kind: "string",
      required: true,
      indexed: true,
      searchable: true,
      sortable: true,
    },
    {
      name: "name",
      label: "Name",
      kind: "string",
      required: true,
      searchable: true,
      sortable: true,
    },
    { name: "phone", label: "Phone", kind: "string", searchable: true },
    { name: "email", label: "Email", kind: "string", searchable: true },
    {
      name: "status",
      label: "Status",
      kind: "enum",
      enumValues: ["draft", "active", "blocked"],
      indexed: true,
      sortable: true,
    },
  ],
  listViews: [
    {
      name: "default",
      label: "Default",
      fields: ["code", "name", "phone", "email", "status"],
      filters: ["code", "name", "phone", "email", "status"],
      defaultSort: "-createdAt",
      maxLimit: 100,
    },
  ],
  workflow: {
    stateField: "status",
    initialState: "draft",
    terminalStates: ["blocked"],
    transitions: [
      {
        action: "activate",
        from: "draft",
        to: "active",
        label: "Activate",
        guard: (data) =>
          typeof data.email === "string" && data.email.length > 0
            ? true
            : "Email is required to activate a customer.",
      },
      { action: "block", from: "active", to: "blocked", label: "Block" },
    ],
  },
};
