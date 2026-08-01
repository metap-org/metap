CREATE TABLE "metadata_versions" (
	"entity_name" varchar(120) PRIMARY KEY NOT NULL,
	"hash" varchar(64) NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL
);
