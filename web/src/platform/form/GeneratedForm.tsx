import { useEffect, useState } from "react";
import { Alert, Button, Container, Stack, Title } from "@mantine/core";
import { useApiQuery } from "../api/useApiQuery";
import { useApiMutation } from "../api/useApiMutation";
import { ApiError } from "../api/client";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useEntity } from "../metadata/useEntity";
import { FieldInput } from "../field/FieldInput";

type RecordDto = {
  id: string;
  version: number;
  data: Record<string, unknown>;
};

export function GeneratedForm({
  entityName,
  recordId,
  onSaved,
}: {
  entityName: string;
  recordId?: string;
  onSaved: (record: RecordDto) => void;
}) {
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  const {
    data: existing,
    isLoading: existingLoading,
    error: existingError,
  } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", entityName, recordId],
    `/api/${entityName}/${recordId}`,
    (response) => response.data,
    Boolean(recordId),
  );

  const [formData, setFormData] = useState<Record<string, unknown>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string[]>>({});

  useEffect(() => {
    if (existing) {
      setFormData(existing.data);
    }
  }, [existing]);

  const createMutation = useApiMutation<{ data: RecordDto }, { data: Record<string, unknown> }>(
    "POST",
    `/api/${entityName}`,
  );
  const updateMutation = useApiMutation<
    { data: RecordDto },
    { version: number; data: Record<string, unknown> }
  >("PATCH", `/api/${entityName}/${recordId}`);

  if (entityLoading || (recordId && existingLoading)) {
    return <div>Loading...</div>;
  }
  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }
  if (recordId && existingError) {
    return <ApiErrorMessage error={existingError} />;
  }
  if (!entity) {
    return <div>Entity not found.</div>;
  }

  function setFieldValue(fieldName: string, value: unknown) {
    setFormData((prev) => ({ ...prev, [fieldName]: value }));
  }

  async function handleSubmit() {
    setFormError(null);
    setFieldErrors({});

    const payload: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(formData)) {
      const field = entity!.fields.find((f) => f.name === key);
      if (field?.kind === "json" && typeof value === "string") {
        try {
          payload[key] = value.trim() === "" ? undefined : JSON.parse(value);
        } catch {
          setFieldErrors({ [key]: ["Invalid JSON"] });
          return;
        }
      } else {
        payload[key] = value;
      }
    }

    try {
      const response = recordId
        ? await updateMutation.mutateAsync({ version: existing!.version, data: payload })
        : await createMutation.mutateAsync({ data: payload });
      onSaved(response.data);
    } catch (error) {
      if (error instanceof ApiError) {
        setFieldErrors(error.fieldErrors ?? {});
        if (!error.fieldErrors) {
          setFormError(error.message);
        }
      } else {
        setFormError("Something went wrong.");
      }
    }
  }

  const submitting = createMutation.isPending || updateMutation.isPending;

  return (
    <Container py="xl" size="sm">
      <Title order={2} mb="md">
        {recordId ? `Edit ${entity.label}` : `New ${entity.label}`}
      </Title>
      {formError ? (
        <Alert color="red" mb="md">
          {formError}
        </Alert>
      ) : null}
      <Stack>
        {entity.fields
          .filter((field) => field.kind !== "id")
          .map((field) => (
            <FieldInput
              key={field.name}
              field={field}
              value={formData[field.name]}
              onChange={(value) => setFieldValue(field.name, value)}
              error={fieldErrors[field.name]?.join(", ")}
            />
          ))}
        <Button onClick={() => void handleSubmit()} loading={submitting}>
          Save
        </Button>
      </Stack>
    </Container>
  );
}
