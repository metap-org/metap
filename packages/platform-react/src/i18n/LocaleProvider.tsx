import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { i18n } from "./i18n";
import { DEFAULT_LOCALE, SUPPORTED_LOCALES } from "./resources";

type LocaleContextValue = {
  locale: string;
  setLocale: (locale: string) => Promise<void>;
};

const LocaleContext = createContext<LocaleContextValue | undefined>(undefined);

function isSupportedLocale(value: string): value is (typeof SUPPORTED_LOCALES)[number] {
  return (SUPPORTED_LOCALES as readonly string[]).includes(value);
}

// Must be nested inside `AuthProvider` — reads `token` to load/persist the caller's
// `GET/PUT /preferences` locale (`crates/metap-http/src/routes/preferences.rs`). Also wraps
// `I18nextProvider` so any consumer of `platform-react` gets a working `useTranslation()`
// without wiring i18next itself.
export function LocaleProvider({ children }: { children: ReactNode }) {
  const { token } = useAuth();
  const [locale, setLocaleState] = useState<string>(DEFAULT_LOCALE);

  useEffect(() => {
    if (!token) {
      return;
    }
    let cancelled = false;
    apiFetch<{ data: { locale: string } }>("/preferences", token)
      .then((response) => {
        if (!cancelled && isSupportedLocale(response.data.locale)) {
          setLocaleState(response.data.locale);
          void i18n.changeLanguage(response.data.locale);
        }
      })
      .catch(() => {
        // No preference saved yet, or the request failed — not user-facing, keep whatever
        // locale is already active.
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const setLocale = useCallback(
    async (next: string) => {
      setLocaleState(next);
      await i18n.changeLanguage(next);
      if (token) {
        await apiFetch("/preferences", token, {
          method: "PUT",
          body: JSON.stringify({ locale: next }),
        });
      }
    },
    [token],
  );

  const value = useMemo(() => ({ locale, setLocale }), [locale, setLocale]);

  return (
    <I18nextProvider i18n={i18n}>
      <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>
    </I18nextProvider>
  );
}

export function useLocale() {
  const context = useContext(LocaleContext);

  if (!context) {
    throw new Error("useLocale must be used within a LocaleProvider");
  }

  return context;
}
