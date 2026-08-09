import { Select } from "@mantine/core";
import { useTranslation } from "react-i18next";
import { useLocale } from "./LocaleProvider";
import { SUPPORTED_LOCALES } from "./resources";

const LOCALE_LABELS: Record<string, string> = { en: "English", vi: "Tiếng Việt" };

export function LocaleSwitcher() {
  const { t } = useTranslation();
  const { locale, setLocale } = useLocale();

  return (
    <Select
      label={t("preferences.locale")}
      data={SUPPORTED_LOCALES.map((code) => ({ value: code, label: LOCALE_LABELS[code] ?? code }))}
      value={locale}
      onChange={(value) => value && void setLocale(value)}
      w={160}
      allowDeselect={false}
    />
  );
}
