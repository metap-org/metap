import { useLocale } from "./LocaleProvider";
import { entityLabelOverrides } from "./entityLabels";

// Must be called inside `LocaleProvider` (same requirement as `useLocale`, which this wraps).
export function useEntityLabels(entityName: string) {
  const { locale } = useLocale();
  const overrides = entityLabelOverrides[locale]?.[entityName];

  return {
    entityLabel: (fallback: string) => overrides?.entity ?? fallback,
    fieldLabel: (fieldName: string, fallback: string) => overrides?.fields?.[fieldName] ?? fallback,
    transitionLabel: (action: string, fallback: string) =>
      overrides?.transitions?.[action] ?? fallback,
  };
}
