import { useEffect, useState } from "react";
import { Alert, Button, Container, Divider, PasswordInput, Stack, TextInput, Title } from "@mantine/core";
import { useTranslation } from "react-i18next";
import { apiFetch, ApiError } from "../api/client";
import { useNavigationAdapter } from "../navigation/NavigationContext";
import { useAuth } from "./AuthContext";

type LoginResponse = { data: { token: string } };
type ProvidersResponse = { data: { providers: string[] } };

type LoginFormProps = {
  /**
   * Optional — most callers (`apps/crm-fe`, `apps/jira-fe` today) omit it and rely on
   * `POST /auth/login`'s global-by-email fallback, unchanged. Pass it once a caller actually
   * has a tenant to log into (e.g. after a tenant-picker step) to also enable the SSO button
   * below, which needs a `tenantId` to know which IdP to redirect to
   * (`GET /auth/oidc/{tenantId}/login`) — there is no tenant-picker UI in this package itself.
   */
  tenantId?: string;
};

export function LoginForm({ tenantId }: LoginFormProps = {}) {
  const { t } = useTranslation();
  const { setToken } = useAuth();
  const navAdapter = useNavigationAdapter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [oidcEnabled, setOidcEnabled] = useState(false);

  useEffect(() => {
    if (!tenantId) return;
    apiFetch<ProvidersResponse>(`/auth/providers?tenantId=${encodeURIComponent(tenantId)}`, null)
      .then((response) => setOidcEnabled(response.data.providers.includes("oidc")))
      .catch(() => setOidcEnabled(false));
  }, [tenantId]);

  async function handleSubmit() {
    setError(null);
    setSubmitting(true);
    try {
      const response = await apiFetch<LoginResponse>("/auth/login", null, {
        method: "POST",
        body: JSON.stringify({ email, password, ...(tenantId ? { tenantId } : {}) }),
      });
      setToken(response.data.token);
      navAdapter.navigate(navAdapter.toHome());
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
        {oidcEnabled && tenantId ? (
          <>
            <Divider label={t("login.orDivider")} labelPosition="center" />
            <Button component="a" href={`/auth/oidc/${encodeURIComponent(tenantId)}/login`} variant="outline">
              {t("login.ssoButton")}
            </Button>
          </>
        ) : null}
      </Stack>
    </Container>
  );
}
