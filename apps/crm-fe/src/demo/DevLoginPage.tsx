import { useState } from "react";
import { Button, Container, Textarea, Title } from "@mantine/core";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "@metap/platform-react";

export function DevLoginPage() {
  const { t } = useTranslation();
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
        {t("devLogin.title")}
      </Title>
      <Textarea
        label={t("devLogin.label")}
        minRows={4}
        value={value}
        onChange={(event) => setValue(event.currentTarget.value)}
      />
      <Button mt="md" onClick={handleSubmit} disabled={value.trim().length === 0}>
        {t("devLogin.useToken")}
      </Button>
    </Container>
  );
}
