// Frontend-only overrides for metadata-authored labels (entity/field/workflow-transition
// names). Deliberately NOT a backend/metadata-model change — `EntityDefinition`'s `label`
// fields stay single-locale strings on the wire (see `docs/roadmap.md` Phase 14's note on why
// that's a separate, bigger decision). A missing locale/entity/field/action here just falls
// back to whatever the backend sent, so this dict only needs entries for labels someone has
// actually bothered to translate — an untranslated entity degrades gracefully to English, it
// doesn't break.
//
// Not self-service: adding a translation means editing this file and shipping a new frontend
// build, not an admin API call. Fine for a demo app (`apps/crm-fe`); a real downstream
// consumer of `platform-react` would maintain its own copy of this shape for its own entities.

export type EntityLabelOverrides = {
  entity?: string;
  fields?: Record<string, string>;
  transitions?: Record<string, string>;
};

/** Plain-function form of `entityLabel`/`fieldLabel`/`transitionLabel` for call sites that
 *  can't use `useEntityLabels` (e.g. rendering a list of *different* entities, where calling
 *  a hook once per row would break the rules of hooks) — pass `locale` from `useLocale()`
 *  once at the top of the component instead. */
export function getEntityLabel(locale: string, entityName: string, fallback: string): string {
  return entityLabelOverrides[locale]?.[entityName]?.entity ?? fallback;
}

export const entityLabelOverrides: Record<string, Record<string, EntityLabelOverrides>> = {
  vi: {
    "crm.customers": {
      entity: "Khách hàng",
      fields: {
        code: "Mã",
        name: "Tên",
        phone: "Số điện thoại",
        email: "Email",
        status: "Trạng thái",
        referredBy: "Người giới thiệu",
      },
      transitions: {
        activate: "Kích hoạt",
        block: "Chặn",
      },
    },
  },
};
