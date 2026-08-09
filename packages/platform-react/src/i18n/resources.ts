// Static UI chrome strings only (buttons, empty/loading/error states) — never entity/field
// labels or metadata-authored content, which stay single-locale strings on `EntityDefinition`
// for now (see `docs/roadmap.md` Phase 14's note on why that's a separate, bigger decision).
// Keep this in sync with `SUPPORTED_LOCALES` in `crates/metap-http/src/routes/preferences.rs`.
//
// Everything below lives under a single `translation` key (i18next's default namespace) —
// `common`/`form`/`error`/etc. are NOT i18next namespaces, they're just nested objects within
// it, addressed with the default `.` key separator (`t("common.actions")`). Giving each of
// them its own top-level key under `en`/`vi` directly (no `translation` wrapper) would make
// i18next treat them as actual namespaces instead, where `.` only nests *within* a namespace
// — `t("common.actions")` would then resolve against the `common` namespace's own `common.actions`
// key, which doesn't exist, and silently render the raw key string instead of throwing.

const en = {
  common: {
    loading: "Loading...",
    loadingMore: "Loading more…",
    somethingWentWrong: "Something went wrong.",
    notFound: "Not found.",
    save: "Save",
    new: "New",
    edit: "Edit",
    delete: "Delete",
    view: "View",
    actions: "Actions",
    any: "Any",
    filterPlaceholder: "Filter...",
    noRecords: "No records.",
    deleteConfirm: "Delete this record? This cannot be undone.",
    invalidJson: "Invalid JSON",
    missingEntityName: "Missing entity name.",
    missingEntityOrId: "Missing entity name or id.",
    entityNotFound: "Entity not found.",
    noListView: "{{label}} has no list view configured.",
  },
  form: {
    editTitle: "Edit {{label}}",
    newTitle: "New {{label}}",
  },
  error: {
    sessionExpired: "Session expired.",
    signInAgain: "Sign in again",
    prefix: "Error: {{message}}",
  },
  workflow: {
    hide: "Hide workflow",
    show: "Show workflow",
    noActions: "No further actions available.",
  },
  devLogin: {
    title: "Dev Login",
    label: "Paste a JWT minted with `pnpm mint-token` (run in the backend repo)",
    useToken: "Use token",
  },
  entities: {
    title: "Entities",
  },
  preferences: {
    locale: "Language",
  },
};

const vi = {
  common: {
    loading: "Đang tải...",
    loadingMore: "Đang tải thêm…",
    somethingWentWrong: "Đã có lỗi xảy ra.",
    notFound: "Không tìm thấy.",
    save: "Lưu",
    new: "Thêm mới",
    edit: "Sửa",
    delete: "Xoá",
    view: "Xem",
    actions: "Hành động",
    any: "Bất kỳ",
    filterPlaceholder: "Lọc...",
    noRecords: "Không có bản ghi nào.",
    deleteConfirm: "Xoá bản ghi này? Không thể hoàn tác.",
    invalidJson: "JSON không hợp lệ",
    missingEntityName: "Thiếu tên entity.",
    missingEntityOrId: "Thiếu tên entity hoặc id.",
    entityNotFound: "Không tìm thấy entity.",
    noListView: "{{label}} chưa cấu hình list view.",
  },
  form: {
    editTitle: "Sửa {{label}}",
    newTitle: "Thêm {{label}}",
  },
  error: {
    sessionExpired: "Phiên đăng nhập đã hết hạn.",
    signInAgain: "Đăng nhập lại",
    prefix: "Lỗi: {{message}}",
  },
  workflow: {
    hide: "Ẩn quy trình",
    show: "Hiện quy trình",
    noActions: "Không còn hành động nào khả dụng.",
  },
  devLogin: {
    title: "Đăng nhập (dev)",
    label: "Dán JWT được tạo bằng `pnpm mint-token` (chạy trong repo backend)",
    useToken: "Dùng token",
  },
  entities: {
    title: "Entities",
  },
  preferences: {
    locale: "Ngôn ngữ",
  },
};

export const resources = {
  en: { translation: en },
  vi: { translation: vi },
} as const;

export const SUPPORTED_LOCALES = Object.keys(resources) as (keyof typeof resources)[];
export const DEFAULT_LOCALE: keyof typeof resources = "en";
