CREATE TABLE "user_preferences" (
	"tenant_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"locale" varchar(20) DEFAULT 'en' NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL,
	PRIMARY KEY ("tenant_id","user_id")
);
