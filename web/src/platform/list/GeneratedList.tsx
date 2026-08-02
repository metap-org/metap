import { useMemo, useState } from "react";
import { useDebouncedValue } from "@mantine/hooks";
import { Container, Select, Table, TextInput, Title } from "@mantine/core";
import { useApiQuery } from "../api/useApiQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { FieldValue } from "../field/FieldValue";
import { useEntity } from "../metadata/useEntity";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

type SortState = { field: string; descending: boolean } | null;

export function GeneratedList({ entityName }: { entityName: string }) {
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  // Text filters are debounced (wait for the user to stop typing before refetching).
  const [filterInputs, setFilterInputs] = useState<Record<string, string>>({});
  // Enum filters come from a Select, not free text, so they refetch immediately on change.
  const [enumFilters, setEnumFilters] = useState<Record<string, string>>({});
  const [sort, setSort] = useState<SortState>(null);
  const [debouncedTextFilters] = useDebouncedValue(filterInputs, 400);

  const listView = entity?.listViews[0];
  const fieldsByName = useMemo(
    () => new Map((entity?.fields ?? []).map((field) => [field.name, field])),
    [entity],
  );

  const activeFilters = useMemo(() => {
    const result: Record<string, string> = {};
    for (const [key, value] of Object.entries(debouncedTextFilters)) {
      if (value.trim().length > 0) {
        result[key] = value.trim();
      }
    }
    for (const [key, value] of Object.entries(enumFilters)) {
      if (value.trim().length > 0) {
        result[key] = value.trim();
      }
    }
    return result;
  }, [debouncedTextFilters, enumFilters]);

  const queryParams = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", String(listView?.maxLimit ?? 30));
    if (sort) {
      params.set("sort", sort.descending ? `-${sort.field}` : sort.field);
    }
    for (const [key, value] of Object.entries(activeFilters)) {
      params.set(key, value);
    }
    return params.toString();
  }, [listView, sort, activeFilters]);

  const {
    data: records,
    isLoading: recordsLoading,
    error: recordsError,
  } = useApiQuery<{ data: RecordDto[] }, RecordDto[]>(
    ["records", entityName, sort, activeFilters],
    `/api/${entityName}?${queryParams}`,
    (response) => response.data,
    Boolean(entity && listView),
  );

  if (entityLoading) {
    return <div>Loading...</div>;
  }

  if (entityError) {
    return <ApiErrorMessage error={entityError} />;
  }

  if (!entity) {
    return <div>Entity not found.</div>;
  }

  if (!listView) {
    return <div>{entity.label} has no list view configured.</div>;
  }

  function toggleSort(field: EntityField) {
    if (!field.sortable) {
      return;
    }

    setSort((current) => {
      if (!current || current.field !== field.name) {
        return { field: field.name, descending: false };
      }

      if (!current.descending) {
        return { field: field.name, descending: true };
      }

      return null;
    });
  }

  const columnCount = listView.fields.length;

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {entity.label}
      </Title>
      <Table>
        <Table.Thead>
          <Table.Tr>
            {listView.fields.map((fieldName) => {
              const field = fieldsByName.get(fieldName);

              if (!field) {
                return <Table.Th key={fieldName} />;
              }

              return (
                <Table.Th
                  key={fieldName}
                  onClick={() => toggleSort(field)}
                  style={{ cursor: field.sortable ? "pointer" : undefined }}
                >
                  {field.label}
                  {sort?.field === fieldName ? (sort.descending ? " ▼" : " ▲") : ""}
                </Table.Th>
              );
            })}
          </Table.Tr>
          <Table.Tr>
            {listView.fields.map((fieldName) => {
              if (!listView.filters.includes(fieldName)) {
                return <Table.Th key={fieldName} />;
              }

              const field = fieldsByName.get(fieldName);

              if (field?.kind === "enum") {
                return (
                  <Table.Th key={fieldName}>
                    <Select
                      placeholder="Any"
                      clearable
                      data={(field.enumValues ?? []).map((value) => ({ value, label: value }))}
                      value={enumFilters[fieldName] || null}
                      onChange={(value) =>
                        setEnumFilters((prev) => ({ ...prev, [fieldName]: value ?? "" }))
                      }
                    />
                  </Table.Th>
                );
              }

              return (
                <Table.Th key={fieldName}>
                  <TextInput
                    placeholder="Filter..."
                    value={filterInputs[fieldName] ?? ""}
                    onChange={(event) => {
                      const value = event.currentTarget.value;
                      setFilterInputs((prev) => ({ ...prev, [fieldName]: value }));
                    }}
                  />
                </Table.Th>
              );
            })}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {recordsLoading ? (
            <Table.Tr>
              <Table.Td colSpan={columnCount}>Loading...</Table.Td>
            </Table.Tr>
          ) : recordsError ? (
            <Table.Tr>
              <Table.Td colSpan={columnCount}>
                <ApiErrorMessage error={recordsError} />
              </Table.Td>
            </Table.Tr>
          ) : records && records.length === 0 ? (
            <Table.Tr>
              <Table.Td colSpan={columnCount}>No records.</Table.Td>
            </Table.Tr>
          ) : (
            records?.map((record) => (
              <Table.Tr key={record.id}>
                {listView.fields.map((fieldName) => {
                  const field = fieldsByName.get(fieldName);

                  return (
                    <Table.Td key={fieldName}>
                      {field ? <FieldValue field={field} value={record.data[fieldName]} /> : null}
                    </Table.Td>
                  );
                })}
              </Table.Tr>
            ))
          )}
        </Table.Tbody>
      </Table>
    </Container>
  );
}
