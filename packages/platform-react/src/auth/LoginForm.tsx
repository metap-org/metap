import { useState } from "react";
import { Alert, Button, Container, PasswordInput, Stack, TextInput, Title } from "@mantine/core";
import { useTranslation } from "react-i18next";
import { apiFetch, ApiError } from "../api/client";
import { useAuth } from "./AuthContext";

type LoginResponse = { data: { token: string } };

export function LoginForm() {
  const { t } = useTranslation();
  const { setToken } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit() {
    setError(null);
    setSubmitting(true);
    try {
      const response = await apiFetch<LoginResponse>("/auth/login", null, {
        method: "POST",
        body: JSON.stringify({ email, password }),
      });
      setToken(response.data.token);
    } catch (err) {
      if (err instanceof ApiError && err.code === "invalid_credentials") {
        setError(t("login.invalidCredentials"));
      } else {
        setError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Container size="xs" py="xl">
      <Title order={2} mb="md">
        {t("login.title")}
      </Title>
      {error ? (
        <Alert color="red" mb="md">
          {error}
        </Alert>
      ) : null}
      <Stack>
        <TextInput
          label={t("login.email")}
          type="email"
          value={email}
          onChange={(event) => setEmail(event.currentTarget.value)}
          onKeyDown={(event) => event.key === "Enter" && void handleSubmit()}
        />
        <PasswordInput
          label={t("login.password")}
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
          onKeyDown={(event) => event.key === "Enter" && void handleSubmit()}
        />
        <Button
          onClick={() => void handleSubmit()}
          loading={submitting}
          disabled={email.trim().length === 0 || password.length === 0}
        >
          {t("login.submit")}
        </Button>
      </Stack>
    </Container>
  );
}
