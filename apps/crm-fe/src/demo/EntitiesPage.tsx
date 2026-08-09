import { Anchor, Container, List, Title } from "@mantine/core";
import { Link } from "react-router-dom";
import { useEntities, ApiErrorMessage } from "@metap/platform-react";

export function EntitiesPage() {
  const { data, isLoading, error } = useEntities();

  if (isLoading) return <div>Loading...</div>;
  if (error) return <ApiErrorMessage error={error} />;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        Entities
      </Title>
      <List>
        {data?.map((entity) => (
          <List.Item key={entity.name}>
            <Anchor component={Link} to={`/records/${entity.name}`}>
              {entity.label} ({entity.name})
            </Anchor>
          </List.Item>
        ))}
      </List>
    </Container>
  );
}
