import { Anchor, Container, Stack, Text, Title } from "@mantine/core";
import { Link } from "react-router-dom";
import { useApiQuery } from "../api/useApiQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useEntity } from "../metadata/useEntity";
import { FieldValue } from "../field/FieldValue";
import { WorkflowActionBar } from "../workflow/WorkflowActionBar";

type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
};

function stateValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function RecordDetail({ entityName, id }: { entityName: string; id: string }) {
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
          onTransitioned={() => {
            void refetch();
          }}
        />
      ) : null}
      <Anchor component={Link} to={`/records/${entityName}/${id}/edit`} mt="md" display="inline-block">
        Edit
      </Anchor>
    </Container>
  );
}
