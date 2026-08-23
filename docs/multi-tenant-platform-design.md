# Định hướng Multi-tenant SaaS (Control Plane + Data Plane)

Ngày: 2026-08-15

Trạng thái: directional (định hướng) — thiết kế đã chốt ở mức tài liệu, **chưa có dòng code
triển khai nào**. Xem `docs/roadmap.md` Phase 16 cho trigger/tình trạng theo dõi, và
[`09. Architecture Decisions`](architectures/09-adr.md) cho các quyết định cốt lõi rút gọn dạng
bullet (bản này là bản chi tiết/lý do đầy đủ phía sau các bullet đó).

## Mục đích

`docs/vision.md` phát biểu định hướng rộng — Metap là backbone tái sử dụng để build business
app, low-code là đích đến cao hơn. `docs/low-code-platform-v1.md` đào sâu đường low-code
(authoring model). Tài liệu này đào sâu một mảnh khác, cụ thể hơn: **làm sao để deploy Metap
như một SaaS multi-tenant thật** (nhiều tenant trên cùng hạ tầng) mà không phá vỡ lời hứa "onboard
dự án outsource nhanh, an toàn kể cả khi delivery ăn xổi" đã nêu ở `docs/team-charter.md`/
`docs/vision.md`.

Phạm vi: tenant isolation, tenant provisioning, control-plane (router/secret/config), data-plane
(storage tier, schema evolution, reconciler DDL online, fan-out multi-tenant), các capability
phái sinh (audit/aggregation/inbound integration), FE onboarding shell, và deployment/HA cho
topology SaaS. Đối chiếu với `docs/architectures/05-building-blocks.md`'s Data Model Strategy
hiện tại (một bảng `records` JSONB dùng chung, isolation app-level `WHERE tenant_id = $`) —
tài liệu này là bước tiến hóa tiếp theo của đúng chiến lược đó khi có tín hiệu scale/SaaS thật,
không phải một redesign từ đầu.

**Nguồn gốc tài liệu:** hợp nhất từ hai bản nháp brainstorm ngày 2026-08-15 — bản đầu đặt cược
vào RLS làm cơ chế isolation chính trên bảng `records` dùng chung; bản sau (nội dung dưới đây)
đổi hướng sang tiered schema/DB-per-tenant sau khi cân nhắc kỹ hơn nhu cầu tách bạch trial/paid
và data-plane ở quy mô 10M+ row/entity. Bản dưới đây là bản chốt duy nhất, thay thế bản đầu.

---

## 1. Nguyên lý nền — cách hòa giải "ngon" vs "ăn xổi"

**Vision:** Low-code multi-tenant, onboard dự án nhanh. Hệ low-code để build business; core
platform + FE để làm dự án outsource "ăn xổi" mà vẫn an toàn.

**Nguyên lý cốt lõi (giải mâu thuẫn "ngon" vs "nhanh"):**

> Dồn toàn bộ rigor vào core engine (viết một lần, kỹ) để tầng dự án ngồi trên có thể ăn xổi mà
> **không thể** gây thảm họa.

Validation, permission, tenant scope, optimistic locking, workflow guard nằm hết ở engine. Dev
delivery dù ẩu tới đâu cũng bị platform chặn sẵn. Đầu tư đúng chỗ: core (ít, ổn định) được làm kỹ
để phần sinh lời (delivery, nhiều, thay đổi liên tục) nhanh một cách an toàn.

**Hai đường authoring, một engine** (đã as-built — xem `docs/roadmap.md` Phase 11):

| | Code-authored | DB-authored (low-code) |
|---|---|---|
| Nguồn | file `.rs` compile vào binary (hoặc codegen từ pack) | row trong `low_code_entities` |
| Đổi schema | rebuild + redeploy | publish runtime, ArcSwap, no restart |
| Dùng cho | dự án outsource một client | business app tenant tự dựng |
| Timing reconcile | offline (build/deploy) | online (publish, hệ đang chạy) |

Cả hai hội tụ tại `EntityDefinition` → cùng pipeline `metadata → CrudService → SQL`.

**Bốn trụ (thứ tự đầu tư):**
1. Tenant isolation cấu trúc (schema/DB boundary — xem §2.1, thay cho đề xuất RLS-only ban đầu)
2. Tenant provisioning nhanh
3. Metadata contract đủ giàu để FE tự sinh
4. Persistence swappable

---

## 2. Control Plane

### 2.1 Tiered tenancy

```
TRIAL          →  schema-per-tenant   (1 DB chung, N schema)
                    provision = CREATE SCHEMA + migrate + seed (nhanh, vài giây)
                    teardown  = DROP SCHEMA (trial hết hạn → xoá sạch tức thì)
                    1 connection pool chung cho mọi trial

PAYING CLIENT  →  DB-per-tenant       (1 DB riêng mỗi client)
                    isolation vật lý, backup/PITR/xoá per client trivial
                    entity nóng = dedicated table trong DB riêng
```

Tenancy row-level → single-tenant = multi-tenant với đúng 1 tenant. Promotion trial→paid =
`pg_dump | psql` + flip registry. RLS **không** còn là trụ #1 (isolation từ schema/DB boundary),
nhưng có thể bật như defense-in-depth.

### 2.2 Router + Control-plane registry

`control.tenants` = nguồn sự thật global (KHÔNG tenant-scoped):

```sql
CREATE TABLE control.tenants (
  id               uuid PRIMARY KEY,
  tier             text NOT NULL,          -- 'trial' | 'paid'
  strategy         text NOT NULL,          -- 'schema' | 'dedicated_db'
  schema_name      text,                   -- 't_ab12'  (strategy=schema)
  dsn_secret_ref   text,                   -- con trỏ tới Vault (strategy=dedicated_db) — KHÔNG chứa password
  status           text NOT NULL,          -- 'provisioning'|'active'|'migrating'|'suspended'|'expired'
  trial_expires_at timestamptz,
  created_at       timestamptz DEFAULT now()
);
```

`RegistryCache` (moka, TTL ~30s, `try_get_with` chống thundering herd, `invalidate` khi
promote/suspend).

`Router.begin(tenant)` — hợp nhất, mọi thứ trong transaction:

```rust
impl Router {
    pub async fn begin(&self, tenant: TenantId) -> Result<Transaction<'_, Postgres>> {
        let routing = self.registry.get(tenant).await?;
        match routing.status {
            TenantStatus::Migrating => bail!(TenantUnavailable::Migrating),  // → 503 retry
            TenantStatus::Suspended => bail!(TenantUnavailable::Suspended),  // → 403
            TenantStatus::Active => {}
        }
        match &routing.strategy {
            Strategy::Schema { schema } => {
                let mut tx = self.shared_pool.begin().await?;
                // SET LOCAL = transaction-scoped, tự dọn khi commit/rollback.
                // schema là SchemaName đã whitelist (^t_[a-z0-9]+$) → an toàn để format.
                sqlx::query(&format!("SET LOCAL search_path TO {}", schema.as_str()))
                    .execute(&mut *tx).await?;
                Ok(tx)
            }
            Strategy::DedicatedDb { dsn_ref } => {
                let pool = self.dedicated_pool(tenant, dsn_ref).await?;  // LRU cache<TenantId, Arc<PgPool>>
                Ok(pool.begin().await?)
            }
        }
    }
}
```

**Bẫy #1 (nghiêm trọng nhất):** `SET LOCAL`/`set_config(_, true)` phải trong transaction, KHÔNG
session-level — pool tái dùng connection sẽ rò tenant/schema sang request sau, **im lặng**
(không crash, chỉ trả sai data). Dùng PgBouncer thì bắt buộc transaction mode.

**Cập nhật triển khai (2026-08-16, Giai đoạn 1):** `crates/metap-control` implement `Router` +
`control.tenants` đúng như trên, và `CrudService` đã refactor để mọi method đi qua
`router.begin(tenant)`. Một khác biệt so với pseudocode gốc: tenant **chưa có row** trong
`control.tenants` không bị coi là lỗi — `Router::begin` fallback về
`{status: Active, strategy: Schema("public")}` (hành vi trước khi có Router). Đây là shim tương
thích ngược có chủ đích, vì chưa có bước provisioning nào ghi vào `control.tenants` cả (mọi
tenant hiện tại chỉ là một UUID bất kỳ trong JWT, không qua đăng ký) — coi tenant chưa đăng ký là
lỗi cứng sẽ phá vỡ toàn bộ dev flow hiện có. Shim này nên được siết lại (bỏ fallback, bắt buộc
row) một khi §2.4 (tenant provisioning) tồn tại và luôn ghi row cho mọi tenant mới.
**Cập nhật triển khai (2026-08-16, Giai đoạn 2):** `TenantStrategy::DedicatedDb` giờ hoạt động —
`crates/metap-control::SecretStore` (trait) + `EnvStore` (impl duy nhất hôm nay: đọc DSN từ biến
env tên đúng bằng `dsn_secret_ref`, không qua Vault) + `Router`'s `dedicated_pools` cache (moka,
idle TTL 10 phút, key theo `dsn_secret_ref`). `dev-tools provision-tenant <id> dedicated_db
<dsnSecretRefName> <dedicatedDatabaseUrl> <adminEmail> <adminPassword>` chạy migration lên DB
riêng, ghi row `control.tenants`, tạo admin user đầu trên DB riêng đó, và in ra biến env cần set
cho tiến trình `crm-server`. Đã verify end-to-end qua HTTP thật: record tạo qua tenant
`dedicated_db` chỉ nằm trong DB riêng, hoàn toàn không xuất hiện ở DB chính.

Ngược lại, `strategy=schema` **vẫn chưa có "răng" thật** — `dev-tools provision-tenant <id> schema
...` luôn ghim `schema_name='public'` (không cho chọn schema khác), vì bảng `records`/`users`/...
chỉ tồn tại ở `public` cho tới khi table-per-entity (§3) triển khai; route một tenant `schema`
sang bất kỳ schema nào khác hôm nay sẽ vỡ với "relation does not exist". Không có `POST
/admin/tenants` qua HTTP — `AdminContext` chỉ ủy quyền hành động trong tenant của chính người gọi
(không có khái niệm "platform superadmin" xuyên tenant), nên provisioning vẫn là CLI-only, cùng
cách `seed-admin`/`create-user` đã giải quyết bài toán con-gà-quả-trứng cho user/role.
`PermissionService::check_action` mặc định **allow** khi entity/action chưa có policy nào — một
tenant mới không có "starter policy" seed sẵn (không khả thi cho code entity-agnostic), nên
`provision-tenant` in cảnh báo rõ thay vì âm thầm bỏ qua.

**Cập nhật triển khai (2026-08-17, Giai đoạn 3):** "khái niệm platform superadmin xuyên tenant"
ở trên giờ tồn tại — không phải bảng/claim mới, mà một tenant sentinel
`metap_control::PLATFORM_TENANT_ID` (`Uuid::nil()`, không bao giờ có row `control.tenants`,
không bao giờ được `Router` route tới) cộng role `"platform_admin"` gán trong tenant đó qua hạ
tầng JWT/role sẵn có. `PlatformAdminContext` (extractor mới, cạnh `AdminContext`) gate route mới
`POST /platform/tenants` (không phải `/admin/tenants` như pseudocode gốc — đặt tên riêng
`/platform/*` để tách bạch rõ khỏi `/admin/*` vốn luôn tenant-scoped). CLI
`dev-tools provision-tenant` và route HTTP này giờ gọi chung
`metap_control::provision_schema_tenant`/`provision_dedicated_db_tenant`, không thể lệch nhau.
`dev-tools bootstrap-platform-admin` bootstrap platform-admin đầu tiên, cùng lý do CLI-only như
`seed-admin`. Chi tiết đầy đủ ở `docs/roadmap.md` Phase 16 Giai đoạn 3.

`CrudService` refactor: mọi query (kể cả read đơn) chạy trên `router.begin(tenant)` thay
`&self.pool`. Metadata cũng per-tenant (`MetadataRouter` cache-per-tenant, vì DB-authored entity
của mỗi tenant nằm trong không gian riêng).

### 2.3 Secret / Config / Env — 3 vòng đời, 3 cửa

| Loại | Ví dụ | Nơi chứa |
|---|---|---|
| **Secret** | DB password, JWT key, API key | **Vault** |
| **Config** | log level, page size, feature flag | **control-plane DB** (4 tầng kế thừa) |
| **Env kỹ thuật** | VAULT_ADDR, AppRole id/secret, control DSN | **env var** (bootstrap) |

**Config 4 tầng kế thừa** (tầng sau đè tầng trước):
```
1. PLATFORM DEFAULT   (code)
2. PROJECT TEMPLATE   ("env default cho dự án OS" — template pack)
3. TENANT             (control-plane DB)
4. RUNTIME OVERRIDE   (env var)
```
Có `provenance` (biết mỗi key từ tầng nào — cho admin UI "kế thừa hay override").

**Vault:** KV static (đơn giản) hoặc **dynamic DB creds** (tự hết hạn, khuyến nghị cho paid).
Auth qua **AppRole** (role_id ~ username, secret_id ~ password inject lúc deploy). Bẫy dynamic ×
pool: creds hết hạn → connection chết loạt → giải bằng **TTL dài (12-24h) + rotate pool trước
hạn**. `SecretStore` trait bọc cả hai + `EnvStore` cho dev.

```rust
#[async_trait]
trait SecretStore: Send + Sync {
    async fn db_credentials(&self, tenant: TenantId) -> Result<DbCreds>;
}
struct DbCreds { dsn: Secret<String>, expires_at: Option<Instant> }  // Some = dynamic → cần rotate
```

### 2.4 Tenant provisioning ("onboard nhanh" chính là đây)

```
POST /admin/tenants  (hoặc CLI: metap provision-tenant)
  → tạo tenant row (control.tenants)
  → seed DEFAULT ROLES: admin, member
  → seed STARTER POLICIES (tránh default-allow trống — điểm #5 review, xem roadmap)
  → tạo admin user đầu (argon2, đã có metap-peripherals::auth)
  → [tùy chọn] apply template pack (entity + policy + config + ui)
  → trả admin credentials + tenant_id
```
Idempotent (chạy lại không nhân đôi). Template pack = một "dự án OS starter" hoàn chỉnh → onboard
= chọn pack + provision.

**Cập nhật triển khai (2026-08-17, Giai đoạn 3):** `POST /platform/tenants` (không phải
`/admin/tenants` — xem §2.2) làm đúng "tạo tenant row" + "tạo admin user đầu" ở trên, cộng
`GET /platform/tenants`/`GET /platform/tenants/{id}` để list/xem, và
`PATCH /platform/tenants/{id}/status` cho suspend/resume (thêm cùng ngày — hoá ra rất nhỏ, vì
`Router::begin` đã reject tenant `Suspended` từ Giai đoạn 1, chỉ thiếu hành động ghi cột
`status`). Chưa làm: seed default roles/starter policies (`PermissionService` vẫn default-allow
cho tenant mới — cảnh báo được in ra, không tự seed), apply template pack (§2.5 chưa tồn tại),
delete/deprovision (cần thiết kế riêng cho việc dọn dữ liệu tenant). Không idempotent theo
nghĩa pseudocode — trùng `tenantId` trả 409 rõ ràng thay vì no-op.

### 2.5 Template Pack (đóng gói)

Pack = thư mục **YAML declarative**:
```
packs/crm-pack/
  pack.toml              # id, version (semver), dependencies (pack phụ thuộc pack)
  entities/*.yaml        # EntityDefinition dạng YAML (map 1:1 struct thật)
  policies/defaults.json
  config/defaults.toml   # tầng 2 config
  secrets/manifest.toml  # KHAI BÁO secret cần có, KHÔNG chứa value
  ui/manifest.yaml       # AppManifest cho FE (mục 9)
  workflows/*.yaml
  i18n/*.json
  migrations/*.sql       # DDL cho dedicated table nếu cần
```

- Immutable theo semver, tenant **ghim version** (không "latest"), pack phụ thuộc pack
  (base-pack).
- `apply_pack` = reconcile idempotent (chạy lại an toàn).
- Nạp 2 đường từ **cùng nguồn**: codegen → `.rs` (compile-in, guard sống) HOẶC seed →
  `low_code_entities` (runtime).
- Pack-owned vs tenant-owned tách bằng **namespace** (`crm.*` vs `custom.*`) → né three-way
  merge.

Ví dụ entity YAML (map 1:1 `EntityDefinition` thật):
```yaml
name: sales.orders
label: Sales Order
storage:
  partition: { by: time, field: createdAt, interval: month }
fields:
  - { name: code,        label: Code,     kind: string,    required: true, unique: true, searchable: true }
  - { name: customer,    label: Customer, kind: reference, required: true,
      refEntity: crm.customers, refDisplayField: name, indexed: true, onDelete: restrict }
  - { name: totalAmount, label: Total,    kind: money,     required: true, sortable: true }
  - { name: status,      label: Status,   kind: enum, enumValues: [draft,confirmed,shipped,cancelled],
      indexed: true, sortable: true }
  - { name: notes,       label: Notes,    kind: string,    searchable: true, searchMode: fts }
listViews:
  - { name: default, label: Default, fields: [code,customer,orderDate,totalAmount,status],
      filters: [code,customer,status], defaultSort: "-orderDate", maxLimit: 100 }
workflow:
  stateField: status
  initialState: draft
  terminalStates: [shipped, cancelled]
  transitions:
    - { action: confirm, from: draft, to: confirmed, label: Confirm,
        guard: { attribute: customer, op: neq, value: { literal: "" } } }   # xem gotcha #2 ở §12
    - { action: ship,    from: confirmed, to: shipped, label: Ship }
    - { action: cancel,  from: draft, to: cancelled, label: Cancel }
```

---

## 3. Data Plane — Storage & Model

### 3.1 Table-per-entity (bắt buộc @ 10M/entity)

Bảng `records` chung **bỏ** cho data nghiệp vụ. Mỗi entity → bảng riêng (mặc định, suy từ
`name`). Lý do @ 10M/entity: N entity × 10M trong một bảng chung = 100M+ → index phình,
autovacuum ác mộng, một entity nặng làm chậm cả hệ.

**MongoDB — cân nhắc nhưng KHÔNG chọn:** JSONB đã schemaless (cái "động" đã có); đổi Mongo mất
transaction/ACID mạnh, mất RLS, phải viết lại `condition_to_sql`/permission-in-query/
optimistic-lock (phần lõi tốt nhất), `$lookup` yếu. Đổi lấy thứ đã có, trả bằng phá giá trị đã
xây.

### 3.2 Ba tier storage (persistence swappable, per-entity)

| Tier | Cấu trúc | Khi nào |
|---|---|---|
| T1 | bảng riêng + `data jsonb` | mặc định, schema động |
| T2 | + generated column cho hot field | field cần index/aggregate |
| T3 | cột thật hoàn toàn | entity cực nóng |

**Field tier SUY từ cờ metadata** (`indexed/sortable/unique/searchable`) — không bắt khai tay:
- `indexed|sortable|unique` → tự promote generated column (planner statistics tốt hơn expression
  index).
- `searchable=substring` → trigram GIN; `searchable=fts` → tsvector generated + GIN.
- Không cờ → ở JSONB (T1).

`FieldStorage { Column, Native }` chỉ là override tường minh.

Map `FieldKind → SQL type`: String/Enum→text, Number→double, **Money→numeric(18,4)** (không
float), Boolean→boolean, Date→date, Datetime→timestamptz, Reference/Id→uuid (mở khóa FK thật),
Json→giữ JSONB.

### 3.3 Relations

Relation = `reference` field (`refEntity`/`refDisplayField`), **không** block riêng. Hai mode:

```yaml
relationMode: referenced   # mặc định: record riêng, query/permission/lifecycle đầy đủ
relationMode: owned        # nested JSONB, atomic, 1 version của cha, chỉ khi con KHÔNG query độc lập
```

**Query-ability quyết mode:** cần query/report trên con → bắt buộc `referenced`. M2M hoãn (tạm
mảng-reference trong JSONB).

**Reference integrity:** tách bảng → **FK thật cấp DB** (`on_delete: Restrict` mặc định). Ref
tới bảng chung → fallback check ở CrudService lúc write. Tách bảng cũng giải query xuyên quan hệ
(JOIN thật).

### 3.4 Perf 10M ≤ 3s — 5 luật

1. **Filterable = phải declared indexed.** Planner từ chối (hoặc gắn slow+timeout) field không
   index. `SET LOCAL statement_timeout='3s'` làm chốt cứng — biến NFR thành invariant DB.
2. **Partial expression index** (`WHERE entity=... AND deleted=false`), build `CONCURRENTLY`,
   sort-index gồm `id` tiebreaker (khớp cursor keyset).
3. **Migration tách vai trò**: indexed field → eager; display field → lazy (§4).
4. **Cursor keyset, KHÔNG OFFSET, KHÔNG COUNT(\*) mặc định** (ước lượng từ `pg_class.reltuples`
   hoặc qua rollup).
5. **owned chỉ cho con không-query.** Entity nóng → generated column, rồi dedicated table.

Con số @ 10M bảng riêng + index đúng: equality 5-30ms, cursor page 20-80ms, FTS 50-200ms — dư
dưới 3s.

---

## 4. Data Plane — Data Evolution

### 4.1 Eager vs Lazy (tách theo vai trò field)

- **Field indexed/promote (cần physical consistency)** → **EAGER backfill + reindex.** Index
  không phủ được hai key (rename) hay hai type → data phải nhất quán vật lý.
- **Field display-only (JSONB, không index)** → **LAZY.** Data mang `_v`; đường đọc áp transform
  in-memory nếu `_v < current`; ghi normalize về current.

### 4.2 Migration = declarative-only, rename tường minh

Không cho code hook (lôi lại bài "custom code" đang tránh). Tập op biểu diễn được + revert được:

```yaml
migrations:
  - op: rename_field    from: totalAmount  to: total
  - op: add_field       name: currency     default: "VND"
  - op: widen_type      field: amount      from: string  to: money   cast: parse_number
  - op: drop_field      field: notes       keep_data: true
  - op: remove_enum     field: status      value: legacy  remap_to: draft   # irreversible
```

**Rename PHẢI tường minh** — diff không phân biệt được "rename a→b" với "drop a + add b". Mỗi op
sinh **cả SQL eager lẫn hàm lazy tương đương** (`to_sql()` + `apply_value()`), test tương đương
bắt buộc khớp (chống lệch semantics SQL vs Rust: float, null, encoding).

`add_field required` không default vào entity đã có data → diff **từ chối** lúc publish (bắt
sớm, không lỗi runtime giữa fan-out).

### 4.3 Transform SQL — ví dụ

```sql
-- rename (idempotent nhờ WHERE data ? 'old')
UPDATE {tbl} SET data = (data - 'totalAmount') || jsonb_build_object('total', data->'totalAmount')
WHERE data ? 'totalAmount';

-- widen string→numeric (CÓ THỂ FAIL trên data bẩn → preflight)
UPDATE {tbl} SET data = jsonb_set(data, '{amount}', to_jsonb((data->>'amount')::numeric))
WHERE jsonb_typeof(data->'amount') = 'string';
```

### 4.4 Preflight & data bẩn

Data hiện có vi phạm ràng buộc mới (cast fail, orphan ref) → **scan đếm TRƯỚC khi migrate**,
không nổ giữa 10M:

```rust
async fn preflight(op) -> PreflightReport {
    // đếm row không cast được / orphan ref — chỉ SELECT count, không UPDATE
    // > 0 → Blocked { bad_rows }
}
```

Policy: `Block` (mặc định, dừng chờ người) | `Coerce { fallback }` | `Quarantine`.

### 4.5 Quarantine

Bảng riêng per-entity giữ `original_data` nguyên vẹn. **Move row bẩn ra TRƯỚC backfill** (không
thì cast fail ngay lô đầu). Resolve đưa row về bảng chính ở **version HIỆN TẠI** (áp full
transform chain). Entity migrate-xong = `done` + backlog quarantine (KHÔNG `error`). Không
quarantine row đang bị ref (tránh orphan mới) và không vì field lazy/display bẩn (chỉ khi chặn
ràng buộc vật lý).

```sql
CREATE TABLE {entity}_quarantine (
  id uuid PRIMARY KEY, tenant_id uuid, original_data jsonb NOT NULL,
  migration_id text, reason text, detail jsonb,
  quarantined_at timestamptz DEFAULT now(), resolved_at timestamptz
);
```

### 4.6 Compaction reaper

Lazy để lại `_v` cũ + phải giữ transform lịch sử → reaper nền hạ-tiên: backfill lười row `_v` cũ
→ cắt transform version cũ. **`min_v` phải gồm cả quarantine table** (chống version cliff làm row
cách ly kẹt). Đọc `current_version` mỗi vòng (chống race publish).

---

## 5. Reconciler — trái tim data-plane

### 5.1 Level-triggered (quyết định gốc)

```
reconcile(desired: EntityDefinition) = introspect(actual) → diff → plan → execute
```

Nhận **trạng thái mong muốn**, hòa giải về đó từ bất kỳ actual nào. Vì DDL online **không
rollback được** (nhiều lệnh CONCURRENTLY ngoài transaction) → "hội tụ dần, chạy lại được" là mô
hình DUY NHẤT khả thi. Cho idempotent + resume sau crash + drift-heal **miễn phí**.

### 5.2 Pipeline

```
EntityDefinition (desired) ──[compile]──► PhysicalSchema(desired) ─┐
                                                                    ├─[diff]─► Vec<DdlOp> ─[execute]─►
Postgres (actual)          ──[introspect]► PhysicalSchema(actual) ─┘   (topo-sort + cost)
```

Cả desired lẫn actual quy về **cùng `PhysicalSchema`** rồi mới diff (không diff EntityDefinition
với pg_catalog trực tiếp).

```rust
struct PhysicalSchema {
    table: String,
    partition: Option<PartitionSpec>,
    columns: BTreeMap<String, ColumnSpec>,      // Framework | Generated(expr) | Native
    indexes: BTreeMap<String, IndexSpec>,       // có cờ `valid` (indisvalid)
    foreign_keys: BTreeMap<String, FkSpec>,     // có cờ `validated` (convalidated)
    uniques: BTreeMap<String, UniqueSpec>,
}
```

### 5.3 Chuẩn hóa (chống loop)

Postgres viết lại biểu thức khi lưu (`pg_get_indexdef` trả dạng canonical khác chữ mình gõ). So
chuỗi thô → false-positive triền miên → reconcile loop vô hạn. **Normalize cả hai phía** trước
khi so (lowercase, bỏ quote, gộp whitespace, bỏ phần schema/table prefix).

### 5.4 Thuật toán diff (các điểm tinh)

```rust
fn diff(desired, actual: Option<&PhysicalSchema>, renames: &[(String,String)]) -> Vec<DdlOp> {
    // 0. Bảng chưa có → CreateTable (partition đặt LÚC NÀY, instant khi rỗng) rồi diff với schema rỗng
    // 1. Áp RENAME TRƯỚC, sửa bản sao `actual` → bước sau không thấy old→new là drop+add
    //    (rename generated col = đổi expr, rẻ; rename jsonb field = data migration Heavy)
    // 2. COLUMNS: thiếu→add, khác→alter, thừa→giữ (chỉ drop nếu bật prune); KHÔNG BAO GIỜ drop cột Framework
    // 3. INDEXES:
    //    - valid && def khớp → skip
    //    - !valid (CONCURRENTLY fail dở) → DROP + CREATE lại  ← resume miễn phí
    //    - valid nhưng def khác → DROP + CREATE
    // 4. FK:
    //    - validated && khớp → skip
    //    - !validated && khớp (crash giữa NOT VALID và VALIDATE) → chỉ VALIDATE  ← resume miễn phí
    // 5. topo_sort: CreateTable → AddColumn → MigrateData → Index → FK NotValid → ValidateFK → Drop
}
```

**Ba điểm sống còn:** (a) rename áp trước + sửa bản sao actual → chống drop+add mất data; (b)
`valid=false` xử như "phải làm lại" → tự lành sau crash; (c) `validated=false` → chỉ VALIDATE,
không add trùng.

FK cross-entity → topo-sort ở tầng orchestrator (reconcile entity được-trỏ trước).

### 5.5 Phân loại cost

```rust
Cost::Instant        // RenameColumn, AddFK NOT VALID (không scan), DropColumn (metadata)
Cost::BackgroundFast // CREATE INDEX CONCURRENTLY, VALIDATE FK (lock nhẹ, cho R/W)
Cost::Heavy          // AddGeneratedColumn (rewrite), MigrateJsonbData (backfill), ConvertPartitioned
```

`entity_is_migrating = ops.any(|o| o.cost() == Heavy)` → quyết bật `Migrating`.

### 5.6 Executor

```rust
async fn execute(ops, ctx) -> Result<()> {
    let _lock = pg_try_advisory_lock(hash(tenant, entity)).await?;   // chống 2 reconcile chồng nhau
    let heavy = ops.iter().any(|o| o.cost() == Heavy);
    if heavy { ctx.set_status(Migrating).await?; }                   // Router → 503 CHỈ cho entity này

    for op in ops {
        match op.execution_mode() {
            Transactional  => run_in_tx(ctx, &op).await?,            // ALTER nhanh
            NonTransactional => run_no_tx(ctx, &op).await?,          // CONCURRENTLY — connection RIÊNG, không begin()
            Batched        => run_heavy(ctx, &op).await?,            // backfill lô
        }
    }
    ctx.commit_metadata().await?;   // GATE: chỉ .store() registry SAU khi DDL xong (bất biến #1)
    if heavy { ctx.set_status(Active).await?; }
}
```

**Bẫy executor:**
- **CONCURRENTLY không chạy trong transaction block.** Connection riêng dùng-một-lần (với
  schema-strategy phải `SET search_path` trực tiếp, connection không về pool chung → tránh rò).
- **Generated column `ADD COLUMN ... GENERATED ... STORED` LUÔN rewrite bảng + ACCESS
  EXCLUSIVE** → trên 10M khóa viết vài phút. **Mặc định né bằng expression index**
  (`CREATE INDEX CONCURRENTLY ((data->>'f')::type)`); chỉ generated column thật khi khai
  `storage:column` + chấp nhận Migrating (expand-contract: add plain col → **bật trigger
  TRƯỚC** → backfill → index).
- **FK trên bảng lớn**: `ADD ... NOT VALID` (instant) rồi tách riêng `VALIDATE CONSTRAINT` (lock
  nhẹ). Data bẩn chặn VALIDATE → dọn trước.

### 5.7 Backfill theo lô (checkpoint + resume)

```rust
async fn run_heavy_backfill(ctx, op) -> Result<()> {
    let mut cursor = ctx.load_progress(&op.op_id).await?.unwrap_or(Uuid::nil());  // RESUME
    loop {
        let mut tx = ctx.router.begin(ctx.tenant).await?;
        let ids = sqlx::query(&format!(
            "WITH batch AS (SELECT id FROM {tbl} WHERE id > $1 ORDER BY id LIMIT 5000)
             UPDATE {tbl} t SET {transform}, _v = $2 FROM batch WHERE t.id = batch.id RETURNING t.id"))
            .bind(cursor).bind(op.target_version).fetch_all(&mut *tx).await?;

        if ids.is_empty() { tx.commit().await?; break; }
        cursor = *ids.iter().max().unwrap();
        ctx.save_progress_tx(&mut tx, &op.op_id, cursor).await?;  // checkpoint CÙNG transaction → atomic

        tx.commit().await?;
        ctx.throttle().await;                    // nhường autovacuum/replication
        if ctx.cancelled() { return Err(Cancelled); }
    }
}
```

**Bốn điểm:** keyset không OFFSET; checkpoint trong cùng transaction với update (atomic → resume
đúng); throttle (chống nghẹt autovacuum); cancellable.

### 5.8 Watchdog

Op Heavy chết → entity kẹt `Migrating` mãi (503 vĩnh viễn). Lease + heartbeat phát hiện; **KHÔNG
rollback** (DDL không cho) → **requeue** để level-triggered làm nốt; trần retry → `Error` rõ ràng
+ alert (không kẹt im lặng).

---

## 6. Orchestrator (fan-out multi-tenant)

### 6.1 Nguồn công việc: version skew

```sql
CREATE TABLE control.entity_deployments (
  tenant_id uuid, entity_name text,
  desired_version bigint NOT NULL,     -- pack/publish set cho MỌI tenant dùng pack (1 UPDATE)
  applied_version bigint,              -- chỉ nhích khi reconcile XONG
  status text, attempts int, last_error text,
  lease_worker text, lease_heartbeat timestamptz,
  PRIMARY KEY (tenant_id, entity_name)
);
```

`desired > applied` = việc cần làm. Cũng là nguồn resume toàn cục (orchestrator chết, khởi động
lại query được còn gì).

### 6.2 Pull-based + SKIP LOCKED

```sql
UPDATE control.entity_deployments SET status='running', lease_worker=$w, attempts=attempts+1
WHERE (tenant_id, entity_name) IN (
  SELECT tenant_id, entity_name FROM control.entity_deployments
  WHERE desired_version > COALESCE(applied_version,0) AND status IN ('pending','failed')
        AND attempts < $max
  ORDER BY (status='failed') ASC, priority_tier ASC, updated_at ASC
  LIMIT $n FOR UPDATE SKIP LOCKED     -- nhiều worker song song KHÔNG trùng
) RETURNING tenant_id, entity_name, desired_version;
```

### 6.3 Concurrency theo resource pool

**Trial (schema chung 1 DB):** 200 CREATE INDEX CONCURRENTLY đồng thời → nghẹt DB chung →
concurrency THẤP (1-2). **Paid (db riêng):** concurrency cao (mỗi db độc lập). Semaphore riêng
per resource.

### 6.4 Failure & rollout

- Phân loại lỗi từ SQLSTATE: **Transient** (deadlock/lock timeout → retry backoff) |
  **DataError** (cast/FK fail → KHÔNG retry, cần sửa data) | **Fatal** (metadata sai → alert
  người).
- **Cô lập per-tenant** — một tenant fail KHÔNG chặn tenant khác (không all-or-nothing). Trạng
  thái hỗn hợp sau đợt là bình thường.
- **Canary → wave rollout**: 1-2 tenant nội bộ → 5% → 25% → 100%; error rate vọt → HALT. Bug
  transform bị bắt ở canary thay vì phá cả fleet.

---

## 7. Testing data-plane

Test cái khó (phân tán/vật lý) trên **Postgres thật (testcontainer), KHÔNG mock**.

**4 invariant bắt buộc:**
1. **Idempotency**: `diff(D, apply(diff(D,A))) == []` (hội tụ 1 pass). Bắt normalize thiếu →
   chống loop vô hạn production.
2. **Resume**: kill ở **mọi step** (chaos, `for kill_after in 0..ops.len()`), chạy lại phải hội
   tụ. Chứng minh "resume miễn phí" của level-triggered là thật.
3. **Eager/lazy equivalence**: property test, `eager(input) == lazy(input)` trên Postgres thật.
   Bắt lệch float/null/encoding + đảm bảo an toàn race compaction.
4. **Tenant isolation**: `max_connections=1` buộc tái dùng connection, hai request khác tenant
   không rò. Bảo vệ trụ isolation.

**NFR bằng guardrail test**: chứng minh "không *thể* query chậm" (planner từ chối field không
index + statement_timeout), không chỉ "query nhanh".

3 tầng: unit (CI mỗi commit) / testcontainer (mỗi PR) / chaos+load 10M (nightly).

---

## 8. Data Plane — Capabilities

### 8.1 Audit / History

Tách rõ: `created_by` (ai chạm cuối) ≠ Outbox (async delivery, có retention) ≠ **History**
(business change log, append-only, giữ lâu).

- **Diff mode mặc định** (snapshot là bom dung lượng @ 10M). Khai **opt-in per-entity**:
  `audit: {enabled, mode, fields, retention}`.
- Ghi **trong cùng transaction** với write — `old` đã fetch sẵn cho optimistic lock → gần free.
- Table **per-entity, partition theo thời gian, append-only** (retention = drop partition,
  không DELETE 50M dòng).
- Diff trên **dạng đã normalize** (ghép lazy). Reference lưu UUID + display snapshot.
- **Audit từ lúc bật** (không backfill quá khứ). Immutability theo compliance: REVOKE
  UPDATE/DELETE → hash-chain (chỉ khi hợp đồng đòi). `actor` từ JWT verified, thời điểm
  server-side.

### 8.2 Aggregation / Reporting

**Capability riêng** (không nhét vào row query), reuse `condition_to_sql` cho WHERE.

@ 10M aggregation **quét không seek** → 3 đòn bẩy:
1. **Measure field promote column** (`SUM` trên numeric thật, không cast mỗi row).
2. **Covering index** cho pattern cố định (`(status) INCLUDE (total)` → index-only scan).
3. **Rollup pre-aggregation** (đòn bẩy thật cho dashboard) — bảng tổng hợp duy trì bằng **async
   qua outbox** (tái dùng hạ tầng, tránh contention của incremental).

**Permission = pushdown WHERE vào aggregation** (thiếu = leak qua số tổng); rollup **dimension
phải gồm trục permission** (user giới hạn đọc được hàng của mình). COUNT né (ước lượng/rollup).
Analytics nặng → **read replica/OLAP**, giữ OLTP primary cho transaction. Metadata-driven:
`aggregatable`/`groupable`/`rollups`.

### 8.3 Inbound Integration

5 tầng: `Adapter → Idempotency Gate → Raw Store → Mapping → Dispatch`.

- **Adapter**: verify **raw body** (không re-serialize), timing-safe compare, timestamp check
  (chống replay), secret per-tenant từ Vault. Verify fail → 401, không lưu.
- **Idempotency Gate**: `UNIQUE(tenant, provider, event_id)` + `INSERT ON CONFLICT DO NOTHING`
  (atomic, chống race). Trả **200 cho event đã Done** (provider ngừng retry).
- **Raw Store**: lưu payload thô TRƯỚC khi xử (replay + audit).
- **Mapping** (declarative): match-before-create, type coercion khai được, **đi QUA Dispatch**
  (không bypass → validation/permission/audit tự áp). Actor = `system:{provider}`.
- **ACK <1s rồi xử async** (tái dùng outbox worker ngược chiều, tránh provider timeout → retry →
  trùng). Event mồ côi/sai thứ tự → quarantine + retry.
- Nhiều nguồn (webhook/poll/file) hội tụ `inbound_events`. Dead letter sau N fail. Routing
  multi-tenant = **URL per tenant**.

```sql
CREATE TABLE inbound_events (
  id bigserial PRIMARY KEY, tenant_id uuid, provider text, event_id text,
  status text,        -- received|processing|done|failed|dead
  raw_payload jsonb, received_at timestamptz DEFAULT now(), attempts int DEFAULT 0,
  UNIQUE (tenant_id, provider, event_id)
);
```

---

## 9. FE Onboarding

Backend metadata-driven nhưng FE hiện **ráp tay** từng dự án (`crm-fe/App.tsx`). Component lá đã
đủ (`GeneratedForm/List`, `RecordDetail`, `FieldInput`, admin pages, `Can`, hooks) — thiếu
**tầng "app metadata" + shell ráp**.

Cần: **`<MetapApp>`** (shell đọc manifest → tự dựng nav + routes + home) + **`AppManifest` (YAML
trong pack) + `/ui-manifest` endpoint** + **branding/theme per tenant** + **slot/override API**
(escape hatch cho 20% bespoke).

Song ánh backend: trial/SaaS = zero-code shell; outsource = `create-metap-app` scaffold +
override. "Chuyển từ code mọi thứ sang code chỉ phần khác biệt."

```tsx
<MetapApp manifest={manifest} metadata={entities}
  overrides={{
    "sales.orders:list": <CustomOrderBoard />,
    routes: [{ path: "/reports/revenue", element: <RevenueReport /> }],
  }} />
```

---

## 10. GraphQL & Microservice

**GraphQL:** adapter transport ngồi trên Dispatch sẵn có, **schema sinh từ metadata**, gần free
— nhưng bắt buộc **DataLoader (chống N+1 @ 10M)** + **complexity/depth limit** (giữ guardrail
3s) + **permission xuyên nested** (field mask mọi tầng resolve). Hoãn tới khi FE thật cần shape
linh hoạt (crate optional sẵn) — xem readiness-note đã có ở `docs/architectures/04-strategy.md`'s
"Sự sẵn sàng của backend cho GraphQL BFF tương lai".

**Microservice: ĐỪNG.** Mâu thuẫn vision outsource/onboard-nhanh (client nhỏ không thể vận hành N
service), mất ACID (audit/outbox/lock cùng transaction), scale trong monolith còn dư địa xa
(table-per-entity + replica + dedicated DB). Đã có thứ đúng hơn: **modular monolith với Dispatch
contract sạch = distributed-ready mà chưa trả giá phân tán.** Tách *một mảnh cụ thể* khi có *tín
hiệu cụ thể*, không phải quyết định trả trước — cùng tinh thần trigger-based của
`docs/roadmap.md` Phase 9.

---

## 11. Deployment Strategy

### 11.1 Artifact & thành phần

```
Rust monolith (metap-http + all crates)   → 1 binary, stateless
Workers (tách hoặc cùng binary flag):
  - outbox-publisher      (DB → RabbitMQ)
  - notification-worker
  - inbound-worker        (inbound_events → dispatch)
  - reconcile-orchestrator (DDL fan-out — TÁCH riêng, xem 11.4)
  - cron (metap-cron)      (SINGLETON — leader election, xem 11.3)
FE: platform-react + app (crm-fe/MetapApp) → static bundle (CDN/nginx)

Data:
  - control-plane DB       (SPOF → HA cao nhất)
  - shared trial DB        (N schema)
  - dedicated paid DBs     (1 per client)
  - read replica(s)        (report/aggregation)

Infra: Vault (SPOF → HA), RabbitMQ, PgBouncer (xem 11.5)
```

### 11.2 Hai topology deployment

```
A. PER-CLIENT (outsource)                B. SaaS MULTI-TENANT

   1 binary + 1 Postgres / client           N app instance (stateless, sau LB)
   docker-compose đủ                         + workers + control-plane + Vault + RabbitMQ
   không cần Vault (env đủ) / cron in-proc   + shared trial DB + dedicated paid DBs
   RLS optional                              + PgBouncer + read replica
   deploy = 1 cục                            + reconcile-orchestrator riêng
```
Cùng binary, khác cách compose. Per-client là chế độ "degenerate" của SaaS (1 tenant, router đơn
giản) → không phải hai codebase.

### 11.3 App instances — stateless + singleton concerns

- **App request-serving: stateless** (state ở Postgres/Vault/control-plane) → scale ngang tự do
  sau load balancer. Không session sticky (JWT stateless, roles resolve mỗi request).
- **Cron PHẢI singleton** — `metap-cron` chạy N lần trên N instance = job chạy trùng N lần. Giải:
  leader election (advisory lock trên control-plane) hoặc dedicated single cron deployment. Đừng
  để cron trong mọi request instance.
- **Workers scale độc lập** — outbox/inbound worker scale theo tải queue, tách khỏi app request
  instance.

### 11.4 Reconcile-orchestrator — TÁCH khỏi request path

DDL fan-out (§6) là **long-running, resource-heavy** (backfill 10M, index CONCURRENTLY vài phút
× N tenant = hàng giờ). **Không** chạy trong instance phục vụ request:
- Deploy như **worker/job riêng** (hoặc k8s Job cho mỗi pack rollout).
- Đọc `control.entity_deployments`, pull-based (SKIP LOCKED cho nhiều orchestrator worker),
  concurrency theo resource pool.
- Instance request-serving chỉ gọi reconcile cho **lowcode publish đơn lẻ** (một entity, một
  tenant, online) — cái này nhẹ, chạy inline được (hoặc đẩy sang orchestrator nếu Heavy).

### 11.5 Connection budget × scale ngang — BẪY LỚN

Router mỗi instance có `shared_pool` + `dedicated LRU pools`. **N instance × pools = nhân lên:**

```
1 instance:  shared 20 + LRU(12 pool × 5) + control 5 = 85 conn
5 instances: 5 × 85 = 425 conn → Postgres max_connections mặc định 100 → SẬP
```

Giải:
1. **PgBouncer (transaction mode) trước MỌI Postgres.** Transaction mode tương thích
   `SET LOCAL`/`set_config(local)` — session mode thì KHÔNG (rò). App connect PgBouncer,
   PgBouncer multiplex xuống ít connection thật. **Bắt buộc khi scale ngang.**
2. **Giảm pool per-instance** — mỗi instance pool nhỏ, dựa PgBouncer gom.
3. **Dedicated DB per paid**: pool riêng cho db đó, không cộng vào budget DB khác — tách tự
   nhiên. Chỉ shared trial DB cần PgBouncer gấp.
4. **Ngân sách tính theo (instances × pools) vs (Postgres max_conn qua PgBouncer)** — luôn tính,
   không đoán.

### 11.6 Zero-downtime app deploy — expand-contract 2 tầng

Rolling deploy → **mixed app version** cùng chạy (v1 và v2 đồng thời). Cần tương thích:

- **Tầng app-schema**: schema thay đổi phải **backward-compatible** (expand-contract): thêm
  cột/field (v1 bỏ qua được) → deploy v2 → v2 dùng → sau khi v1 hết → contract (bỏ cũ). Không
  đổi/xóa thứ v1 đang dùng trong một deploy.
- **Tầng reconcile**: Add field → DDL trước, deploy app dùng. Remove field → deploy app (ngừng
  dùng) → DDL sau (drop). Thứ tự expand-contract.
- **Health/readiness**: instance chỉ nhận traffic khi kết nối được control-plane + Vault
  (bootstrap xong). Fail → không ready, LB không route.

### 11.7 Bootstrap sequence (mỗi instance)

```
1. Đọc env: VAULT_ADDR, VAULT_ROLE_ID/SECRET_ID, CONTROL_DB_DSN, RUST_LOG
2. AppRole login → Vault token (renew nền)
3. Đọc platform secret (JWT key) + connect control-plane
4. Load platform defaults + template packs (config tầng 1-2)
5. Warm RegistryCache (lazy — không preload mọi tenant)
6. Readiness = OK → nhận traffic
```

### 11.8 HA & Failure domains

- **Control-plane DB + Vault là SPOF** — HA cao nhất (Vault raft ≥3 node, control-plane
  primary+standby). Cache trong app (RegistryCache TTL, Vault creds cache) chịu blip ngắn — đừng
  gọi mỗi request.
- **Vault auto-unseal** (KMS cloud/transit) — không nhập key tay mỗi restart.
- **Shared trial DB down** → mọi trial down (chấp nhận). **Dedicated paid DB down** → chỉ 1
  client down (isolation — lợi thế).
- **RabbitMQ down** → outbox tích lũy (đã persist trong DB), publish khi lại — không mất event.

### 11.9 Backup / DR (lợi thế tiered tenancy)

- **Paid (dedicated DB)**: backup/PITR **per client** trivial. GDPR "xóa data" = drop DB. Data
  residency = DB region khác. Restore một client không đụng client khác.
- **Trial (schema)**: backup DB chung; xóa trial = DROP SCHEMA.
- **Control-plane**: backup riêng, tần suất cao + PITR (mất nó = mất routing mọi tenant).
- **Vault**: raft snapshot.

### 11.10 Observability (per-tenant)

- Metrics/trace/log gắn `tenant_id` + `request_id` (mầm `telemetry.rs`) → debug per-tenant, cost
  attribution, phát hiện noisy neighbor.
- Inbound: rate/fail/lag theo tenant+provider.
- Reconcile: dashboard ma trận `desired/applied version`, entity `Migrating`/`Error`.
- Per-tenant rate limit (một provider spam không làm ngập worker tenant khác).

---

## 12. Cross-cutting gotchas (nhớ kỹ)

1. **`SET LOCAL`/`set_config(_, true)` trong transaction**, không session — pool tái dùng rò
   tenant/schema **im lặng**. Nghiêm trọng nhất. PgBouncer phải transaction mode.
2. **Workflow `guard` là `#[serde(skip)]`** → chết khi round-trip serde; DB-authored chưa hỗ trợ
   workflow → pack có guard chỉ nạp compile-in cho tới "Phase B" (xem `docs/roadmap.md` Phase 11).
3. **Validation chỉ check type, không format/default** → pack chỉ khai cái engine đọc.
4. **Gate metadata swap SAU DDL** — swap trước khi cột tồn tại = vỡ entity.
5. **DDL online KHÔNG rollback** → an toàn dựa hoàn toàn vào level-triggered + checkpoint.
6. **Control-plane + Vault là SPOF** → HA + cache chịu blip.
7. **Connection budget nhân theo số instance** → PgBouncer bắt buộc khi scale ngang.
8. **Cron/orchestrator không được chạy trên mọi instance** → singleton/tách riêng.

---

## Phụ lục — `EntityDefinition` mở rộng (tham chiếu)

Struct thật hiện nay ở `crates/metap-metadata/src/entity.rs`; các trường dưới đây là phần **MỞ
RỘNG** mà thiết kế trên giả định tồn tại — chưa có trong code:

```rust
enum FieldKind { Id, String, Number, Boolean, Date, Datetime, Money, Enum, Reference, Json }

struct EntityField {
    name, label: String, kind: FieldKind,
    required, indexed, unique, searchable, sortable: Option<bool>,
    enum_values: Option<Vec<String>>,
    ref_entity, ref_display_field: Option<String>,
    search_mode: Option<String>,        // "substring" | "fts"
    // MỞ RỘNG (storage): storage: Option<FieldStorage>, on_delete: Option<OnDelete>
}
struct EntityWorkflow {
    state_field, initial_state: String, terminal_states: Vec<String>,
    transitions: Vec<WorkflowTransition>,   // guard: #[serde(skip)] — xem gotcha #2
}
struct EntityDefinition {
    name, label, table_name: String,
    fields: Vec<EntityField>, list_views: Vec<EntityListView>,
    workflow: Option<EntityWorkflow>,
    // MỞ RỘNG: storage: StorageConfig { table, partition }, audit: AuditConfig, rollups: Vec<Rollup>
}
```

*Tài liệu phản ánh các quyết định tính tới thời điểm biên soạn (2026-08-15). Các phần "MỞ RỘNG"
là thiết kế đã chốt nhưng chưa có trong code. Xem `docs/roadmap.md` Phase 16 cho trạng thái triển
khai.*
