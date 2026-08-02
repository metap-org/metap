import { relations, sql } from "drizzle-orm";
import {
  boolean,
  index,
  integer,
  jsonb,
  pgTable,
  text,
  timestamp,
  uniqueIndex,
  uuid,
  varchar,
} from "drizzle-orm/pg-core";

export const records = pgTable(
  "records",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    entity: varchar("entity", { length: 120 }).notNull(),
    code: varchar("code", { length: 120 }),
    status: varchar("status", { length: 80 }),
    data: jsonb("data")
      .notNull()
      .default(sql`'{}'::jsonb`),
    version: integer("version").notNull().default(1),
    deleted: boolean("deleted").notNull().default(false),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
    createdBy: uuid("created_by"),
    updatedBy: uuid("updated_by"),
  },
  (table) => ({
    tenantEntityStatusIdx: index("records_tenant_entity_status_idx").on(
      table.tenantId,
      table.entity,
      table.status,
    ),
    tenantEntityCreatedIdx: index("records_tenant_entity_created_idx").on(
      table.tenantId,
      table.entity,
      table.createdAt,
    ),
  }),
);

export const outboxEvents = pgTable(
  "outbox_events",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    topic: varchar("topic", { length: 200 }).notNull(),
    aggregateType: varchar("aggregate_type", { length: 120 }).notNull(),
    aggregateId: uuid("aggregate_id").notNull(),
    payload: jsonb("payload").notNull(),
    publishedAt: timestamp("published_at", { withTimezone: true }),
    attempts: integer("attempts").notNull().default(0),
    lastError: text("last_error"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (table) => ({
    unpublishedIdx: index("outbox_events_unpublished_idx").on(table.publishedAt, table.createdAt),
  }),
);

export const workflowEvents = pgTable(
  "workflow_events",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    entity: varchar("entity", { length: 120 }).notNull(),
    recordId: uuid("record_id").notNull(),
    action: varchar("action", { length: 80 }).notNull(),
    fromState: varchar("from_state", { length: 80 }).notNull(),
    toState: varchar("to_state", { length: 80 }).notNull(),
    actor: uuid("actor"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (table) => ({
    tenantEntityRecordIdx: index("workflow_events_tenant_entity_record_idx").on(
      table.tenantId,
      table.entity,
      table.recordId,
      table.createdAt,
    ),
  }),
);

export const userRoles = pgTable(
  "user_roles",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    userId: uuid("user_id").notNull(),
    role: varchar("role", { length: 80 }).notNull(),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    createdBy: uuid("created_by"),
  },
  (table) => ({
    tenantUserRoleUnique: uniqueIndex("user_roles_tenant_user_role_unique").on(
      table.tenantId,
      table.userId,
      table.role,
    ),
    tenantUserIdx: index("user_roles_tenant_user_idx").on(table.tenantId, table.userId),
  }),
);

export const policies = pgTable(
  "policies",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    entity: varchar("entity", { length: 120 }).notNull(),
    action: varchar("action", { length: 20 }).notNull(),
    field: varchar("field", { length: 120 }),
    subject: varchar("subject", { length: 20 }).notNull().default("context"),
    roles: jsonb("roles"),
    condition: jsonb("condition"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    createdBy: uuid("created_by"),
  },
  (table) => ({
    tenantEntityActionIdx: index("policies_tenant_entity_action_idx").on(
      table.tenantId,
      table.entity,
      table.action,
    ),
  }),
);

export const metadataVersions = pgTable("metadata_versions", {
  entityName: varchar("entity_name", { length: 120 }).primaryKey(),
  hash: varchar("hash", { length: 64 }).notNull(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
});

export const recordRelations = relations(records, () => ({}));
