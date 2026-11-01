CREATE TABLE "policies" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"tenant_id" uuid NOT NULL,
	"entity" varchar(120) NOT NULL,
	"action" varchar(20) NOT NULL,
	"roles" jsonb,
	"condition" jsonb,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	"created_by" uuid
);
--> statement-breakpoint
CREATE INDEX "policies_tenant_entity_action_idx" ON "policies" USING btree ("tenant_id","entity","action");