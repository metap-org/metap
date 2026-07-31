import { Container, Table, Title } from "@mantine/core";
import { useApiQuery } from "../platform/api/useApiQuery";
import { ApiErrorMessage } from "../platform/api/ApiErrorMessage";

type CustomerRecord = {
  id: string;
  code: string | null;
  status: string | null;
  data: { name?: string };
};

export function CustomersPage() {
  const { data, isLoading, error } = useApiQuery<{ data: CustomerRecord[] }, CustomerRecord[]>(
    ["records", "crm.customers"],
    "/api/crm.customers?limit=30",
    (response) => response.data,
  );

  if (isLoading) return <div>Loading...</div>;
  if (error) return <ApiErrorMessage error={error} />;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        Customers
      </Title>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Code</Table.Th>
            <Table.Th>Name</Table.Th>
            <Table.Th>Status</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {data?.map((record) => (
            <Table.Tr key={record.id}>
              <Table.Td>{record.code}</Table.Td>
              <Table.Td>{record.data.name}</Table.Td>
              <Table.Td>{record.status}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Container>
  );
}
