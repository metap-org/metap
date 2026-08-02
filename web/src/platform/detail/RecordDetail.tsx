import { useState } from "react";
import { Alert, Anchor, Button, Container, Group, Stack, Text, Title } from "@mantine/core";
import { Link, useNavigate } from "react-router-dom";
import { useApiQuery } from "../api/useApiQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { ApiError, apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { useEntity } from "../metadata/useEntity";
import { FieldValue } from "../field/FieldValue";
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
  const { token } = useAuth();
  const navigate = useNavigate();
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
    if (!record || !window.confirm("Delete this record? This cannot be undone.")) {
      return;
    }

    setDeleteError(null);
    setDeleting(true);
    try {
      await apiFetch(`/api/${entityName}/${id}`, token, {
        method: "DELETE",
        body: JSON.stringify({ version: record.version }),
      });
      navigate(`/records/${entityName}`);
    } catch (error) {
      setDeleteError(error instanceof ApiError ? error.message : "Something went wrong.");
      setDeleting(false);
    }
  }

  if (entityLoading || recordLoading) {
    return <div>Loading...</div>;
  }
  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }
  if (recordError) {
    return <ApiErrorMessage error={recordError} />;
  }
  if (!entity || !record) {
    return <div>Not found.</div>;
  }

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {entity.label}
      </Title>
      <Stack mb="md">
        {entity.fields
          .filter((field) => field.kind !== "id")
          .map((field) => (
            <div key={field.name}>
              <Text size="sm" fw={500}>
                {field.label}
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
        <Anchor component={Link} to={`/records/${entityName}/${id}/edit`}>
          Edit
        </Anchor>
        <Button color="red" variant="subtle" size="compact-sm" loading={deleting} onClick={() => void handleDelete()}>
          Delete
        </Button>
      </Group>
    </Container>
  );
}
