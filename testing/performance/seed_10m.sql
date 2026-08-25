-- Manual, out-of-band seed for
-- `crates/metap-crud/tests/crud_service_postgres.rs`'s
-- `sustained_concurrent_list_across_many_tenants_at_ten_million_rows` (the test whose numbers
-- are recorded in `testing/performance/baseline.md`) — NOT run in CI or automatically by
-- `run-nightly-benchmark.sh`. 10M rows takes real time/disk on a real machine; this stays a
-- documented, reproducible manual step, same posture `baseline.md` itself takes ("cập nhật file
-- này bằng tay mỗi khi chạy lại benchmark").
--
-- Shape must exactly match what that test expects (its own comments spell this out): 10 tenants,
-- each with 200 `hr.departments`, 2,000 `hr.employees`, 1,000,000 `helpdesk.tickets` — 10M
-- ticket rows total. Every run adds 10 *new* tenants (fresh `gen_random_uuid()`s) rather than
-- upserting into existing ones — re-running doubles the data, it doesn't refresh it. Clean up an
-- old run with `DELETE FROM records WHERE tenant_id = ANY('{...}'::uuid[])` if disk matters.
--
-- Usage:
--   psql "$DATABASE_URL" -f testing/performance/seed_10m.sql
--
-- Reference values (`departmentId`/`assigneeId`/`reporterId`) are stored as plain text UUIDs in
-- `data jsonb`, matching how every non-dedicated-table `Reference` field is actually stored
-- (`jsonb_extract_path_text`-readable, not a typed column) — the same shape `CrudService` writes
-- for a real `hr.employees`/`helpdesk.tickets` record through the API, just inserted directly
-- for seeding speed instead of going through 10M individual `CrudService::create` calls.
DO $$
DECLARE
  seed_tenant_id uuid;
  dept_ids uuid[];
  employee_ids uuid[];
  t int;
BEGIN
  FOR t IN 1..10 LOOP
    seed_tenant_id := gen_random_uuid();

    INSERT INTO records (tenant_id, entity, data)
    SELECT seed_tenant_id, 'hr.departments', jsonb_build_object('name', 'Dept ' || i)
    FROM generate_series(1, 200) AS i;

    dept_ids := ARRAY(
      SELECT id FROM records WHERE tenant_id = seed_tenant_id AND entity = 'hr.departments'
    );

    INSERT INTO records (tenant_id, entity, data)
    SELECT
      seed_tenant_id,
      'hr.employees',
      jsonb_build_object(
        'userId', 'user-' || i,
        'name', 'Employee ' || i,
        'departmentId', (dept_ids[1 + floor(random() * array_length(dept_ids, 1))::int])::text
      )
    FROM generate_series(1, 2000) AS i;

    employee_ids := ARRAY(
      SELECT id FROM records WHERE tenant_id = seed_tenant_id AND entity = 'hr.employees'
    );

    INSERT INTO records (tenant_id, entity, data)
    SELECT
      seed_tenant_id,
      'helpdesk.tickets',
      jsonb_build_object(
        'title', 'Ticket ' || i,
        'description', 'Auto-generated ticket #' || i || ' for perf seed',
        'status', (ARRAY['open', 'in_progress', 'resolved', 'closed'])[1 + floor(random() * 4)::int],
        'priority', (ARRAY['low', 'medium', 'high', 'urgent'])[1 + floor(random() * 4)::int],
        'assigneeId', (employee_ids[1 + floor(random() * array_length(employee_ids, 1))::int])::text,
        'reporterId', (employee_ids[1 + floor(random() * array_length(employee_ids, 1))::int])::text,
        'departmentId', (dept_ids[1 + floor(random() * array_length(dept_ids, 1))::int])::text
      )
    FROM generate_series(1, 1000000) AS i;

    RAISE NOTICE 'seeded tenant % (%/10)', seed_tenant_id, t;
  END LOOP;
END $$;
