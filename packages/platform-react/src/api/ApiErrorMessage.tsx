import { useTranslation } from "react-i18next";
import { useNavigationAdapter } from "../navigation/NavigationContext";
import { ApiError } from "./client";

export function ApiErrorMessage({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const adapter = useNavigationAdapter();

  if (error instanceof ApiError && error.status === 401) {
    return (
      <div>
        {t("error.sessionExpired")}{" "}
        <adapter.Link to={adapter.toLogin()}>{t("error.signInAgain")}</adapter.Link>.
      </div>
    );
  }

  return (
    <div>
      {t("error.prefix", { message: error instanceof Error ? error.message : String(error) })}
    </div>
  );
}
