import { Anchor, Container, Group, List, Title } from "@mantine/core";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useEntities, ApiErrorMessage, LocaleSwitcher, getEntityLabel, useLocale } from "@metap/platform-react";

export function EntitiesPage() {
  const { t } = useTranslation();
  const { locale } = useLocale();
  const { data, isLoading, error } = useEntities();

  if (isLoading) return <div>{t("common.loading")}</div>;
  if (error) return <ApiErrorMessage error={error} />;

  return (
    <Container py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>{t("entities.title")}</Title>
        <LocaleSwitcher />
      </Group>
      <List>
        {data?.map((entity) => (
          <List.Item key={entity.name}>
            <Anchor component={Link} to={`/records/${entity.name}`}>
              {getEntityLabel(locale, entity.name, entity.label)} ({entity.name})
            </Anchor>
          </List.Item>
        ))}
      </List>
    </Container>
  );
}
