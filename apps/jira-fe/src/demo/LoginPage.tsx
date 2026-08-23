import { useState } from "react";
import { Alert, Button, Container, Stack, Textarea, Title } from "@mantine/core";
import { LoginForm, useAuth } from "@metap/platform-react";
import { useNavigate } from "react-router-dom";

/**
 * `LoginForm`'s `POST /auth/login` queries `AppState.pool` (the platform's own database), not
 * this tenant's dedicated one — a pre-existing, documented gap (`apps/jira-server/src/main.rs`'s
 * top doc comment): a `DedicatedDb` tenant's `users` row is unreachable through that route today.
 * Real login stays here for when that gap closes; this fallback lets a token minted out-of-band
 * (`pnpm mint:jira-token`) unblock using the app in the meantime — dev-only, not a real auth path.
 */
function PasteTokenFallback() {
  const { setToken } = useAuth();
  const navigate = useNavigate();
  const [value, setValue] = useState("");

  return (
    <Container size="xs" pb="xl">
      <Alert color="yellow" title="Dev-only: real login can't reach this tenant yet">
        This tenant runs on its own dedicated database, and `/auth/login` isn&apos;t tenant-routed
        yet — see <code>apps/jira-server/src/main.rs</code>. Mint a token with{" "}
        <code>pnpm mint:jira-token &lt;tenantId&gt; &lt;userId&gt;</code> and paste it below
        instead.
      </Alert>
      <Stack mt="md">
        <Textarea
          label="JWT"
          autosize
          minRows={2}
          value={value}
          onChange={(event) => setValue(event.currentTarget.value)}
        />
        <Button
          onClick={() => {
            setToken(value.trim());
            navigate("/");
          }}
          disabled={value.trim().length === 0}
        >
          Use token
        </Button>
      </Stack>
    </Container>
  );
}

export function LoginPage() {
  return (
    <>
      <LoginForm />
      <Container size="xs" pb="md">
        <Title order={4} mb="sm">
          — or —
        </Title>
      </Container>
      <PasteTokenFallback />
    </>
  );
}
