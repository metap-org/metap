import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useEntities, ApiErrorMessage, getEntityLabel, useLocale } from "@metap/platform-ui";

export function EntitiesPage() {
  const { t } = useTranslation();
  const { locale } = useLocale();
  const { data, isLoading, error } = useEntities();

  if (isLoading) return <div>{t("common.loading")}</div>;
  if (error) return <ApiErrorMessage error={error} />;

  return (
    <div className="mx-auto max-w-3xl py-8">
      <h2 className="mb-4 text-xl font-semibold text-foreground">{t("entities.title")}</h2>
      <ul className="flex list-disc flex-col gap-1 pl-5">
        {data?.map((entity) => (
          <li key={entity.name}>
            <Link
              to={`/records/${entity.name}`}
              className="text-sm font-medium text-primary underline-offset-2 hover:underline"
            >
              {getEntityLabel(locale, entity.name, entity.label)} ({entity.name})
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}
