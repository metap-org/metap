-- Roadmap 114 P3: a role's permissions can grant single workflow actions, stored as
-- `transition:<action>` (permission.TransitionActionPolicy). varchar(20) only fit the five base
-- actions — `transition:markFalsePositive` is 28. Widened in the shared policies table and in every
-- tenant schema's copy.
DO $$
DECLARE
  tbl text;
BEGIN
  FOR tbl IN
    SELECT quote_ident(table_schema) || '.policies'
    FROM information_schema.tables
    WHERE table_name = 'policies' AND table_type = 'BASE TABLE'
  LOOP
    EXECUTE format('ALTER TABLE %s ALTER COLUMN action TYPE varchar(120)', tbl);
  END LOOP;
END $$;
