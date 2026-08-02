import { useState } from "react";
import { Button, Container, Textarea, Title } from "@mantine/core";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@metap/platform-react";

export function DevLoginPage() {
  const [value, setValue] = useState("");
  const { setToken } = useAuth();
  const navigate = useNavigate();

  function handleSubmit() {
    setToken(value.trim());
    navigate("/");
  }

  return (
    <Container size="sm" py="xl">
      <Title order={2} mb="md">
        Dev Login
      </Title>
      <Textarea
        label="Paste a JWT minted with `pnpm mint-token` (run in the backend repo)"
        minRows={4}
        value={value}
        onChange={(event) => setValue(event.currentTarget.value)}
      />
      <Button mt="md" onClick={handleSubmit} disabled={value.trim().length === 0}>
        Use token
      </Button>
    </Container>
  );
}
