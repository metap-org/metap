CREATE TABLE "low_code_entity_drafts" (
	"entity_name" varchar(200) PRIMARY KEY,
	"definition" jsonb NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "low_code_entity_versions" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"entity_name" varchar(200) NOT NULL,
	"definition" jsonb NOT NULL,
	"version_number" integer NOT NULL,
	"published_at" timestamp with time zone DEFAULT now() NOT NULL,
	"restored_from_version" integer,
	CONSTRAINT "low_code_entity_versions_entity_version_unique" UNIQUE ("entity_name","version_number")
);
--> statement-breakpoint
CREATE INDEX "low_code_entity_versions_entity_idx" ON "low_code_entity_versions" USING btree ("entity_name");
