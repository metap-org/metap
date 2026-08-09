import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import { DEFAULT_LOCALE, resources } from "./resources";

// A dedicated instance (not the `i18next` default-export singleton) so a host app embedding
// `platform-react` alongside its own i18next setup can't collide with it.
export const i18n = i18next.createInstance();

void i18n.use(initReactI18next).init({
  resources,
  lng: DEFAULT_LOCALE,
  fallbackLng: DEFAULT_LOCALE,
  // "translation" (i18next's built-in default namespace) — see resources.ts's top comment
  // for why `common`/`form`/etc. must NOT be namespaces themselves.
  interpolation: { escapeValue: false }, // React already escapes.
});

export default i18n;
