CREATE TABLE "workflow_events" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"tenant_id" uuid NOT NULL,
	"entity" varchar(120) NOT NULL,
	"record_id" uuid NOT NULL,
	"action" varchar(80) NOT NULL,
	"from_state" varchar(80) NOT NULL,
	"to_state" varchar(80) NOT NULL,
	"actor" uuid,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE INDEX "workflow_events_tenant_entity_record_idx" ON "workflow_events" USING btree ("tenant_id","entity","record_id","created_at");