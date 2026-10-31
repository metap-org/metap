-- Closes the gap 0028 deliberately left open: `records`/`attachments`/`_sqlx_migrations` stayed
-- in `public` while every other framework table moved to `metadata`. Safe the same way 0028 was —
-- the database-level default `search_path` it set (`public, metadata, control`) already falls
-- through to `metadata` for any unqualified reference once a table isn't in `public` anymore, and
-- `Router::begin`'s own `SET LOCAL search_path TO {schema_name}, metadata, control` does too. No
-- Rust code was ever schema-qualifying these three to `public` specifically except
-- `metap-control::tenant_schema::TENANT_SCOPED_TABLES`, updated alongside this migration.
--
-- Moving `_sqlx_migrations` itself here, in a migration it will track running: the `ALTER TABLE`
-- below takes effect immediately (DDL is visible within the same transaction that ran it), so
-- sqlx's own post-migration `INSERT INTO _sqlx_migrations` for *this* file still resolves —
-- `public` no longer has the table, `metadata` (second in the default search_path) does.
ALTER TABLE public.records SET SCHEMA metadata;
ALTER TABLE public.attachments SET SCHEMA metadata;
ALTER TABLE public._sqlx_migrations SET SCHEMA metadata;
