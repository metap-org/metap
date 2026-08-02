import { useEffect, useMemo, useRef, useState } from "react";
import { useDebouncedValue } from "@mantine/hooks";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  Alert,
  Anchor,
  Button,
  Container,
  Group,
  Select,
  Table,
  TextInput,
  Title,
} from "@mantine/core";
import { useApiInfiniteQuery } from "../api/useApiInfiniteQuery";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import { ApiError, apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { FieldValue } from "../field/FieldValue";
import { useEntity } from "../metadata/useEntity";
import type { EntityField } from "../metadata/types";
import { useNavigationAdapter } from "../navigation/NavigationContext";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

type ListPage = {
  data: RecordDto[];
  page: { limit: number; nextCursor: string | null };
};

type SortState = { field: string; descending: boolean } | null;

const ROW_HEIGHT = 40;

export function GeneratedList({ entityName }: { entityName: string }) {
  const { token } = useAuth();
  const navAdapter = useNavigationAdapter();
  const { data: entity, isLoading: entityLoading, error: entityError } = useEntity(entityName);
  // Text filters are debounced (wait for the user to stop typing before refetching).
  const [filterInputs, setFilterInputs] = useState<Record<string, string>>({});
  // Enum filters come from a Select, not free text, so they refetch immediately on change.
  const [enumFilters, setEnumFilters] = useState<Record<string, string>>({});
  const [sort, setSort] = useState<SortState>(null);
  const [debouncedTextFilters] = useDebouncedValue(filterInputs, 400);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);

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

  const baseParams = useMemo(() => {
    const params = new URLSearchParams();
    params.set("limit", String(listView?.maxLimit ?? 30));
    if (sort) {
      params.set("sort", sort.descending ? `-${sort.field}` : sort.field);
    }
    for (const [key, value] of Object.entries(activeFilters)) {
      params.set(key, value);
    }
    return params;
  }, [listView, sort, activeFilters]);

  const {
    data,
    isLoading: recordsLoading,
    error: recordsError,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
    refetch,
  } = useApiInfiniteQuery<ListPage>(
    ["records", entityName, sort, activeFilters],
    (cursor) => {
      const params = new URLSearchParams(baseParams);
      if (cursor) {
        params.set("cursor", cursor);
      }
      return `/api/${entityName}?${params.toString()}`;
    },
    (lastPage) => lastPage.page.nextCursor,
    Boolean(entity && listView),
  );

  const records = useMemo(() => data?.pages.flatMap((page) => page.data) ?? [], [data]);

  const scrollContainerRef = useRef<HTMLDivElement>(null);

  const rowVirtualizer = useVirtualizer({
    count: records.length,
    getScrollElement: () => scrollContainerRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  });

  const virtualRows = rowVirtualizer.getVirtualItems();
  const lastVirtualIndex = virtualRows[virtualRows.length - 1]?.index;

  useEffect(() => {
    if (lastVirtualIndex === undefined) {
      return;
    }
    if (lastVirtualIndex >= records.length - 10 && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [lastVirtualIndex, records.length, hasNextPage, isFetchingNextPage, fetchNextPage]);

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

  async function handleDelete(record: RecordDto) {
    if (!window.confirm("Delete this record? This cannot be undone.")) {
      return;
    }

    setDeleteError(null);
    setPendingDeleteId(record.id);
    try {
      await apiFetch(`/api/${entityName}/${record.id}`, token, {
        method: "DELETE",
        body: JSON.stringify({ version: record.version }),
      });
      await refetch();
    } catch (error) {
      setDeleteError(error instanceof ApiError ? error.message : "Something went wrong.");
    } finally {
      setPendingDeleteId(null);
    }
  }

  const columnCount = listView.fields.length + 1;

  return (
    <Container py="xl">
      <Group justify="space-between" mb="md">
        <Title order={2}>{entity.label}</Title>
        <Button component={navAdapter.Link} to={navAdapter.toNewRecord(entityName)}>
          New
        </Button>
      </Group>
      {deleteError ? (
        <Alert color="red" mb="md" onClose={() => setDeleteError(null)} withCloseButton>
          {deleteError}
        </Alert>
      ) : null}
      <div ref={scrollContainerRef} style={{ height: 600, overflow: "auto" }}>
        <Table>
          <Table.Thead
            style={{
              position: "sticky",
              top: 0,
              zIndex: 1,
              background: "var(--mantine-color-body)",
            }}
          >
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
              <Table.Th>Actions</Table.Th>
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
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody style={{ position: "relative", height: rowVirtualizer.getTotalSize() }}>
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
            ) : records.length === 0 ? (
              <Table.Tr>
                <Table.Td colSpan={columnCount}>No records.</Table.Td>
              </Table.Tr>
            ) : (
              virtualRows.map((virtualRow) => {
                const record = records[virtualRow.index];

                if (!record) {
                  return null;
                }

                return (
                  <Table.Tr
                    key={record.id}
                    style={{
                      position: "absolute",
                      transform: `translateY(${virtualRow.start}px)`,
                      width: "100%",
                    }}
                  >
                    {listView.fields.map((fieldName) => {
                      const field = fieldsByName.get(fieldName);

                      return (
                        <Table.Td key={fieldName}>
                          {field ? (
                            <FieldValue field={field} value={record.data[fieldName]} />
                          ) : null}
                        </Table.Td>
                      );
                    })}
                    <Table.Td>
                      <Group gap="xs" wrap="nowrap">
                        <Anchor
                          component={navAdapter.Link}
                          to={navAdapter.toRecordDetail(entityName, record.id)}
                        >
                          View
                        </Anchor>
                        <Button
                          color="red"
                          variant="subtle"
                          size="compact-sm"
                          loading={pendingDeleteId === record.id}
                          disabled={pendingDeleteId !== null && pendingDeleteId !== record.id}
                          onClick={() => void handleDelete(record)}
                        >
                          Delete
                        </Button>
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                );
              })
            )}
          </Table.Tbody>
        </Table>
        {isFetchingNextPage ? (
          <div style={{ padding: 8, textAlign: "center" }}>Loading more…</div>
        ) : null}
      </div>
    </Container>
  );
}
