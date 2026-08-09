import { useState } from "react";
import { Alert, Anchor, Button, Container, Group, Stack, Text, Title } from "@mantine/core";
import { useTranslation } from "react-i18next";
import { useApiQuery } from "../api/useApiQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { ApiError, apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { useEntity } from "../metadata/useEntity";
import { FieldValue } from "../field/FieldValue";
import { useEntityLabels } from "../i18n/useEntityLabels";
import { useNavigationAdapter } from "../navigation/NavigationContext";
import { WorkflowActionBar } from "../workflow/WorkflowActionBar";
import type { RecordCapabilities } from "./recordCapabilities";

type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
  capabilities: RecordCapabilities;
};

function stateValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function RecordDetail({ entityName, id }: { entityName: string; id: string }) {
  const { t } = useTranslation();
  const { entityLabel, fieldLabel } = useEntityLabels(entityName);
  const { token } = useAuth();
  const navAdapter = useNavigationAdapter();
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  const {
    data: record,
    isLoading: recordLoading,
    error: recordError,
    refetch,
  } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", entityName, id],
    `/api/${entityName}/${id}`,
    (response) => response.data,
  );

  async function handleDelete() {
    if (!record || !window.confirm(t("common.deleteConfirm"))) {
      return;
    }

    setDeleteError(null);
    setDeleting(true);
    try {
      await apiFetch(`/api/${entityName}/${id}`, token, {
        method: "DELETE",
        body: JSON.stringify({ version: record.version }),
      });
      navAdapter.navigate(navAdapter.toRecordList(entityName));
    } catch (error) {
      setDeleteError(error instanceof ApiError ? error.message : t("common.somethingWentWrong"));
      setDeleting(false);
    }
  }

  if (entityLoading || recordLoading) {
    return <div>{t("common.loading")}</div>;
  }
  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }
  if (recordError) {
    return <ApiErrorMessage error={recordError} />;
  }
  if (!entity || !record) {
    return <div>{t("common.notFound")}</div>;
  }

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {entityLabel(entity.label)}
      </Title>
      <Stack mb="md">
        {entity.fields
          .filter((field) => field.kind !== "id")
          .map((field) => (
            <div key={field.name}>
              <Text size="sm" fw={500}>
                {fieldLabel(field.name, field.label)}
              </Text>
              <FieldValue field={field} value={record.data[field.name]} />
            </div>
          ))}
      </Stack>
      {entity.workflow ? (
        <WorkflowActionBar
          entityName={entityName}
          recordId={id}
          version={record.version}
          workflow={entity.workflow}
          currentState={stateValue(record.data[entity.workflow.stateField])}
          capabilities={record.capabilities}
          onTransitioned={() => {
            void refetch();
          }}
        />
      ) : null}
      {deleteError ? (
        <Alert color="red" mt="md" onClose={() => setDeleteError(null)} withCloseButton>
          {deleteError}
        </Alert>
      ) : null}
      <Group mt="md">
        <Anchor component={navAdapter.Link} to={navAdapter.toEditRecord(entityName, id)}>
          {t("common.edit")}
        </Anchor>
        <Button
          color="red"
          variant="subtle"
          size="compact-sm"
          loading={deleting}
          onClick={() => void handleDelete()}
        >
          {t("common.delete")}
        </Button>
      </Group>
    </Container>
  );
}
