CREATE TABLE "low_code_metadata_audit_events" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"entity_name" varchar(200) NOT NULL,
	"action" varchar(20) NOT NULL,
	"actor_user_id" text,
	"actor_tenant_id" text NOT NULL,
	"version_number" integer,
	"restored_from_version" integer,
	"occurred_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE INDEX "low_code_metadata_audit_events_entity_idx" ON "low_code_metadata_audit_events" USING btree ("entity_name", "occurred_at");
