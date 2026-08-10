import { useState } from "react";
import {
  Alert,
  Button,
  Code,
  Container,
  Group,
  Select,
  Stack,
  Table,
  Text,
  Textarea,
  TextInput,
  Title,
} from "@mantine/core";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api/client";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useEntities } from "../metadata/useEntities";
import { useEntity } from "../metadata/useEntity";
import { useAdminPolicies, useCreateAdminPolicy, useDeleteAdminPolicy } from "./adminApi";

const ACTIONS = ["read", "create", "update", "delete", "write"];
const NO_FIELD = "";

export function PoliciesAdminPage() {
  const { t } = useTranslation();
  const { data: policies, isLoading, error, refetch } = useAdminPolicies();
  const { data: entities } = useEntities();
  const createPolicy = useCreateAdminPolicy();
  const deletePolicy = useDeleteAdminPolicy();

  const [entity, setEntity] = useState("");
  const [action, setAction] = useState<string>(ACTIONS[0]!);
  const [roles, setRoles] = useState("");
  const [field, setField] = useState(NO_FIELD);
  const { data: selectedEntity } = useEntity(entity);
  const fieldOptions = [
    { value: NO_FIELD, label: t("admin.policies.fieldNone") },
    ...(selectedEntity?.fields.map((f) => ({ value: f.name, label: f.label })) ?? []),
  ];
  const [subject, setSubject] = useState<string>("context");
  const [conditionText, setConditionText] = useState("");
  const [conditionError, setConditionError] = useState<string | null>(null);
  const [rowError, setRowError] = useState<string | null>(null);

  async function handleCreate() {
    setConditionError(null);
    let condition: unknown;
    if (conditionText.trim().length > 0) {
      try {
        condition = JSON.parse(conditionText);
      } catch {
        setConditionError(t("common.invalidJson"));
        return;
      }
    }

    try {
      await createPolicy.mutateAsync({
        entity,
        action,
        roles: roles
          .split(",")
          .map((r) => r.trim())
          .filter(Boolean),
        field: field.trim().length > 0 ? field.trim() : undefined,
        subject,
        condition,
      });
      setEntity("");
      setAction(ACTIONS[0]!);
      setRoles("");
      setField(NO_FIELD);
      setConditionText("");
      await refetch();
    } catch {
      // surfaced via createPolicy.error below
    }
  }

  async function handleDelete(id: string) {
    if (!window.confirm(t("common.deleteConfirm"))) {
      return;
    }
    setRowError(null);
    try {
      await deletePolicy(id);
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {t("admin.policies.title")}
      </Title>

      <Stack mb="xl" maw={480}>
        <Title order={4}>{t("admin.policies.createTitle")}</Title>
        {createPolicy.error ? (
          <Alert color="red">
            {createPolicy.error instanceof ApiError
              ? createPolicy.error.message
              : t("common.somethingWentWrong")}
          </Alert>
        ) : null}
        <Select
          label={t("admin.policies.entity")}
          data={(entities ?? []).map((e) => ({ value: e.name, label: e.label }))}
          value={entity || null}
          onChange={(value) => {
            setEntity(value ?? "");
            setField(NO_FIELD);
          }}
          searchable
          placeholder={t("admin.policies.entityPlaceholder")}
        />
        <Select
          label={t("admin.policies.action")}
          data={ACTIONS}
          value={action}
          onChange={(value) => setAction(value ?? ACTIONS[0]!)}
          allowDeselect={false}
        />
        <TextInput
          label={t("admin.users.rolesLabel")}
          description={t("admin.users.rolesDescription")}
          value={roles}
          onChange={(event) => setRoles(event.currentTarget.value)}
        />
        <Select
          label={t("admin.policies.field")}
          description={t("admin.policies.fieldDescription")}
          data={fieldOptions}
          value={field}
          disabled={entity.length === 0}
          onChange={(value) => setField(value ?? NO_FIELD)}
          allowDeselect={false}
        />
        <Select
          label={t("admin.policies.subject")}
          data={[
            { value: "context", label: "context" },
            { value: "record", label: "record" },
          ]}
          value={subject}
          onChange={(value) => setSubject(value ?? "context")}
          allowDeselect={false}
        />
        <Textarea
          label={t("admin.policies.condition")}
          description={t("admin.policies.conditionDescription")}
          value={conditionText}
          onChange={(event) => setConditionText(event.currentTarget.value)}
          error={conditionError}
          autosize
          minRows={2}
        />
        <Button
          onClick={() => void handleCreate()}
          loading={createPolicy.isPending}
          disabled={entity.trim().length === 0 || action.trim().length === 0}
        >
          {t("common.new")}
        </Button>
      </Stack>

      {rowError ? (
        <Alert color="red" mb="md" onClose={() => setRowError(null)} withCloseButton>
          {rowError}
        </Alert>
      ) : null}

      {isLoading ? (
        <Text>{t("common.loading")}</Text>
      ) : error ? (
        <ApiErrorMessage error={error} />
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t("admin.policies.entity")}</Table.Th>
              <Table.Th>{t("admin.policies.action")}</Table.Th>
              <Table.Th>{t("admin.policies.field")}</Table.Th>
              <Table.Th>{t("admin.policies.subject")}</Table.Th>
              <Table.Th>{t("admin.users.rolesLabel")}</Table.Th>
              <Table.Th>{t("admin.policies.condition")}</Table.Th>
              <Table.Th>{t("common.actions")}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(policies ?? []).map((policy) => (
              <Table.Tr key={policy.id}>
                <Table.Td>{policy.entity}</Table.Td>
                <Table.Td>{policy.action}</Table.Td>
                <Table.Td>{policy.field ?? "—"}</Table.Td>
                <Table.Td>{policy.subject}</Table.Td>
                <Table.Td>{(policy.roles ?? []).join(", ") || "—"}</Table.Td>
                <Table.Td>
                  {policy.condition ? (
                    <Code block style={{ maxWidth: 260, whiteSpace: "pre-wrap" }}>
                      {JSON.stringify(policy.condition)}
                    </Code>
                  ) : (
                    "—"
                  )}
                </Table.Td>
                <Table.Td>
                  <Group gap="xs" wrap="nowrap">
                    <Button
                      color="red"
                      variant="subtle"
                      size="compact-sm"
                      onClick={() => void handleDelete(policy.id)}
                    >
                      {t("common.delete")}
                    </Button>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Container>
  );
}
