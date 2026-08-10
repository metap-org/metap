import { useState } from "react";
import { Alert, Badge, Button, Container, Group, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api/client";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { useAdminRoleActions, useAdminUsers, useCreateAdminUser } from "./adminApi";

export function UsersAdminPage() {
  const { t } = useTranslation();
  const { data: users, isLoading, error, refetch } = useAdminUsers();
  const createUser = useCreateAdminUser();
  const { assignRole, revokeRole } = useAdminRoleActions();

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [roles, setRoles] = useState("");
  const [roleInputs, setRoleInputs] = useState<Record<string, string>>({});
  const [rowError, setRowError] = useState<string | null>(null);

  async function handleCreate() {
    setRowError(null);
    try {
      await createUser.mutateAsync({
        email,
        password,
        roles: roles
          .split(",")
          .map((r) => r.trim())
          .filter(Boolean),
      });
      setEmail("");
      setPassword("");
      setRoles("");
      await refetch();
    } catch {
      // surfaced via createUser.error below
    }
  }

  async function handleAssign(userId: string) {
    const role = (roleInputs[userId] ?? "").trim();
    if (!role) {
      return;
    }
    setRowError(null);
    try {
      await assignRole(userId, role);
      setRoleInputs((prev) => ({ ...prev, [userId]: "" }));
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  async function handleRevoke(userId: string, role: string) {
    setRowError(null);
    try {
      await revokeRole(userId, role);
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {t("admin.users.title")}
      </Title>

      <Stack mb="xl" maw={480}>
        <Title order={4}>{t("admin.users.createTitle")}</Title>
        {createUser.error ? (
          <Alert color="red">
            {createUser.error instanceof ApiError
              ? createUser.error.message
              : t("common.somethingWentWrong")}
          </Alert>
        ) : null}
        <TextInput
          label={t("login.email")}
          type="email"
          value={email}
          onChange={(event) => setEmail(event.currentTarget.value)}
        />
        <TextInput
          label={t("login.password")}
          type="password"
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
        />
        <TextInput
          label={t("admin.users.rolesLabel")}
          description={t("admin.users.rolesDescription")}
          value={roles}
          onChange={(event) => setRoles(event.currentTarget.value)}
        />
        <Button
          onClick={() => void handleCreate()}
          loading={createUser.isPending}
          disabled={email.trim().length === 0 || password.length === 0}
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
              <Table.Th>{t("admin.users.userId")}</Table.Th>
              <Table.Th>{t("admin.users.rolesLabel")}</Table.Th>
              <Table.Th>{t("admin.users.assignRole")}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(users ?? []).map((user) => (
              <Table.Tr key={user.userId}>
                <Table.Td>{user.userId}</Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {user.roles.map((role) => (
                      <Badge
                        key={role}
                        variant="light"
                        rightSection={
                          <Text
                            component="span"
                            size="xs"
                            style={{ cursor: "pointer" }}
                            onClick={() => void handleRevoke(user.userId, role)}
                          >
                            ×
                          </Text>
                        }
                      >
                        {role}
                      </Badge>
                    ))}
                  </Group>
                </Table.Td>
                <Table.Td>
                  <Group gap="xs" wrap="nowrap">
                    <TextInput
                      size="xs"
                      placeholder={t("admin.users.rolesLabel")}
                      value={roleInputs[user.userId] ?? ""}
                      onChange={(event) => {
                        const value = event.currentTarget.value;
                        setRoleInputs((prev) => ({ ...prev, [user.userId]: value }));
                      }}
                    />
                    <Button size="xs" variant="light" onClick={() => void handleAssign(user.userId)}>
                      {t("admin.users.assignRole")}
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
