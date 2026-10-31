ALTER TABLE "policies" ADD COLUMN "field" varchar(120);--> statement-breakpoint
ALTER TABLE "policies" ADD COLUMN "subject" varchar(20) DEFAULT 'context' NOT NULL;