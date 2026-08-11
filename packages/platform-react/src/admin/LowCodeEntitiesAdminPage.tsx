import type { Dispatch, SetStateAction } from "react";
import { Fragment, memo, useCallback, useMemo, useState } from "react";
import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Checkbox,
  Container,
  Group,
  MultiSelect,
  NumberInput,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
} from "@mantine/core";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api/client";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import {
  useLowCodeActions,
  useLowCodeEntities,
  useLowCodeVersions,
  type LowCodeVersionSummary,
} from "./adminApi";

// Every FieldKind `metap_metadata::FieldKind` declares except "id" — the id column is
// implicit/system-managed (`records.id`), never something an author picks for a new field.
const FIELD_KINDS = [
  "string",
  "number",
  "boolean",
  "date",
  "datetime",
  "money",
  "enum",
  "reference",
  "json",
];

type FieldRow = {
  name: string;
  label: string;
  kind: string;
  required: boolean;
  searchable: boolean;
  sortable: boolean;
  enumValues: string; // comma-separated — only meaningful when kind === "enum"
  refEntity: string; // only meaningful when kind === "reference"
};

function emptyFieldRow(): FieldRow {
  return {
    name: "",
    label: "",
    kind: "string",
    required: false,
    searchable: false,
    sortable: false,
    enumValues: "",
    refEntity: "",
  };
}

/** Wire shape is `metap_metadata::EntityField` (camelCase JSON) — matches
 * `crates/metap-metadata/src/entity.rs`. Optional flags are only emitted when true/non-empty,
 * mirroring that struct's `#[serde(skip_serializing_if = "Option::is_none")]` fields, so a
 * freshly-built field doesn't carry a pile of `false`/empty noise the server never asked for. */
function fieldRowToWire(row: FieldRow): Record<string, unknown> {
  const wire: Record<string, unknown> = {
    name: row.name.trim(),
    label: row.label.trim(),
    kind: row.kind,
  };
  if (row.required) wire.required = true;
  if (row.searchable) wire.searchable = true;
  if (row.sortable) wire.sortable = true;
  if (row.kind === "enum") {
    wire.enumValues = row.enumValues
      .split(",")
      .map((v) => v.trim())
      .filter(Boolean);
  }
  if (row.kind === "reference" && row.refEntity.trim().length > 0) {
    wire.refEntity = row.refEntity.trim();
  }
  return wire;
}

function wireToFieldRow(field: unknown): FieldRow {
  const f = (field ?? {}) as Record<string, unknown>;
  return {
    name: typeof f.name === "string" ? f.name : "",
    label: typeof f.label === "string" ? f.label : "",
    kind: typeof f.kind === "string" ? f.kind : "string",
    required: f.required === true,
    searchable: f.searchable === true,
    sortable: f.sortable === true,
    enumValues: Array.isArray(f.enumValues) ? f.enumValues.join(", ") : "",
    refEntity: typeof f.refEntity === "string" ? f.refEntity : "",
  };
}

/** Memoized so editing/toggling one field row doesn't force every *other* row's `Select`
 * (Mantine's combobox/floating-ui positioning is not free) to re-render too — `onUpdate`/
 * `onRemove` are stable (`useCallback` in `FieldBuilder`) and `row` only gets a new reference
 * when *this* row's own data actually changes, so `memo`'s shallow prop comparison correctly
 * skips unrelated rows. This is the fix for the multi-second lag reported when toggling
 * `required`/`searchable` with several fields in the table — every checkbox click was
 * re-rendering the entire table, including every other row's dropdown. */
const FieldRowEditor = memo(function FieldRowEditor({
  row,
  index,
  onUpdate,
  onRemove,
}: {
  row: FieldRow;
  index: number;
  onUpdate: (index: number, patch: Partial<FieldRow>) => void;
  onRemove: (index: number) => void;
}) {
  const { t } = useTranslation();

  return (
    <Table.Tr>
      <Table.Td>
        <TextInput
          size="xs"
          value={row.name}
          onChange={(e) => onUpdate(index, { name: e.currentTarget.value })}
        />
      </Table.Td>
      <Table.Td>
        <TextInput
          size="xs"
          value={row.label}
          onChange={(e) => onUpdate(index, { label: e.currentTarget.value })}
        />
      </Table.Td>
      <Table.Td>
        <Select
          size="xs"
          data={FIELD_KINDS}
          value={row.kind}
          onChange={(value) => onUpdate(index, { kind: value ?? "string" })}
          allowDeselect={false}
          w={110}
        />
      </Table.Td>
      <Table.Td>
        <Checkbox
          checked={row.required}
          onChange={(e) => onUpdate(index, { required: e.currentTarget.checked })}
        />
      </Table.Td>
      <Table.Td>
        <Checkbox
          checked={row.searchable}
          onChange={(e) => onUpdate(index, { searchable: e.currentTarget.checked })}
        />
      </Table.Td>
      <Table.Td>
        <Checkbox
          checked={row.sortable}
          onChange={(e) => onUpdate(index, { sortable: e.currentTarget.checked })}
        />
      </Table.Td>
      <Table.Td>
        {row.kind === "enum" ? (
          <TextInput
            size="xs"
            placeholder={t("admin.lowcode.enumValuesPlaceholder")}
            value={row.enumValues}
            onChange={(e) => onUpdate(index, { enumValues: e.currentTarget.value })}
          />
        ) : row.kind === "reference" ? (
          <TextInput
            size="xs"
            placeholder={t("admin.lowcode.refEntityPlaceholder")}
            value={row.refEntity}
            onChange={(e) => onUpdate(index, { refEntity: e.currentTarget.value })}
          />
        ) : (
          "—"
        )}
      </Table.Td>
      <Table.Td>
        <ActionIcon color="red" variant="subtle" onClick={() => onRemove(index)}>
          ×
        </ActionIcon>
      </Table.Td>
    </Table.Tr>
  );
});

function FieldBuilder({
  fields,
  onChange,
}: {
  fields: FieldRow[];
  onChange: Dispatch<SetStateAction<FieldRow[]>>;
}) {
  const { t } = useTranslation();

  // Stable across renders (deps on `onChange`, which is `setFields` and never changes) —
  // the functional-update form means these never need `fields` itself as a dependency, so
  // every `FieldRowEditor` always receives the *same* callback reference. See that
  // component's doc comment for why this matters for perf.
  const updateRow = useCallback(
    (index: number, patch: Partial<FieldRow>) => {
      onChange((prev) => prev.map((row, i) => (i === index ? { ...row, ...patch } : row)));
    },
    [onChange],
  );

  const removeRow = useCallback(
    (index: number) => {
      onChange((prev) => prev.filter((_, i) => i !== index));
    },
    [onChange],
  );

  const addRow = useCallback(() => {
    onChange((prev) => [...prev, emptyFieldRow()]);
  }, [onChange]);

  return (
    <Stack gap="xs">
      <Text size="sm" fw={500}>
        {t("admin.lowcode.fields")}
      </Text>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t("admin.lowcode.fieldName")}</Table.Th>
            <Table.Th>{t("admin.lowcode.fieldLabel")}</Table.Th>
            <Table.Th>{t("admin.lowcode.fieldKind")}</Table.Th>
            <Table.Th>{t("admin.lowcode.fieldRequired")}</Table.Th>
            <Table.Th>{t("admin.lowcode.fieldSearchable")}</Table.Th>
            <Table.Th>{t("admin.lowcode.fieldSortable")}</Table.Th>
            <Table.Th>{t("admin.lowcode.fieldExtra")}</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {fields.length === 0 ? (
            <Table.Tr>
              <Table.Td colSpan={8}>{t("admin.lowcode.noFields")}</Table.Td>
            </Table.Tr>
          ) : (
            fields.map((row, index) => (
              <FieldRowEditor
                key={index}
                row={row}
                index={index}
                onUpdate={updateRow}
                onRemove={removeRow}
              />
            ))
          )}
        </Table.Tbody>
      </Table>
      <Group>
        <Button variant="default" size="xs" onClick={addRow}>
          {t("admin.lowcode.addField")}
        </Button>
      </Group>
    </Stack>
  );
}

// Always resolvable regardless of declared fields — mirrors
// `crates/metap-metadata/src/compiler.rs`'s `IMPLICIT_SYSTEM_FIELDS`, which
// `compiler::validate` treats as known field names for `listViews`/`defaultSort` purposes.
const IMPLICIT_SYSTEM_FIELDS = ["createdAt", "updatedAt"];

type ListViewRow = {
  name: string;
  label: string;
  fields: string[];
  filters: string[];
  sortField: string; // "" = no default sort
  sortDesc: boolean;
  maxLimit: number;
};

function emptyListViewRow(): ListViewRow {
  return {
    name: "default",
    label: "",
    fields: [],
    filters: [],
    sortField: "",
    sortDesc: false,
    maxLimit: 50,
  };
}

/** Wire shape is `metap_metadata::EntityListView` — `defaultSort` is a single string, `-field`
 * for descending (see `crates/metap-query/src/query_planner.rs`'s sort parsing), not a
 * separate direction field — `sortField`/`sortDesc` only exist as two form inputs, combined
 * into one string here. */
function listViewRowToWire(row: ListViewRow): Record<string, unknown> {
  const wire: Record<string, unknown> = {
    name: row.name.trim(),
    label: row.label.trim(),
    fields: row.fields,
    filters: row.filters,
    maxLimit: row.maxLimit,
  };
  if (row.sortField.trim().length > 0) {
    wire.defaultSort = row.sortDesc ? `-${row.sortField}` : row.sortField;
  }
  return wire;
}

function wireToListViewRow(view: unknown): ListViewRow {
  const v = (view ?? {}) as Record<string, unknown>;
  const defaultSort = typeof v.defaultSort === "string" ? v.defaultSort : "";
  const sortDesc = defaultSort.startsWith("-");
  return {
    name: typeof v.name === "string" ? v.name : "",
    label: typeof v.label === "string" ? v.label : "",
    fields: Array.isArray(v.fields)
      ? v.fields.filter((f): f is string => typeof f === "string")
      : [],
    filters: Array.isArray(v.filters)
      ? v.filters.filter((f): f is string => typeof f === "string")
      : [],
    sortField: sortDesc ? defaultSort.slice(1) : defaultSort,
    sortDesc,
    maxLimit: typeof v.maxLimit === "number" ? v.maxLimit : 50,
  };
}

/** Memoized for the same reason as `FieldRowEditor` — `fieldNames`/`sortOptions` only get a
 * new reference when the underlying field list actually changes (`useMemo` in the parent), so
 * an edit in one list-view card doesn't re-render every other card's `MultiSelect`. */
const ListViewRowEditor = memo(function ListViewRowEditor({
  row,
  index,
  fieldNames,
  sortOptions,
  onUpdate,
  onRemove,
}: {
  row: ListViewRow;
  index: number;
  fieldNames: string[];
  sortOptions: { value: string; label: string }[];
  onUpdate: (index: number, patch: Partial<ListViewRow>) => void;
  onRemove: (index: number) => void;
}) {
  const { t } = useTranslation();

  return (
    <Stack
      gap="xs"
      p="sm"
      style={{ border: "1px solid var(--mantine-color-gray-4)", borderRadius: 4 }}
    >
      <Group align="flex-end">
        <TextInput
          style={{ flex: 1 }}
          size="xs"
          label={t("admin.lowcode.listViewName")}
          value={row.name}
          onChange={(e) => onUpdate(index, { name: e.currentTarget.value })}
        />
        <TextInput
          style={{ flex: 1 }}
          size="xs"
          label={t("admin.lowcode.listViewLabel")}
          value={row.label}
          onChange={(e) => onUpdate(index, { label: e.currentTarget.value })}
        />
        <ActionIcon color="red" variant="subtle" onClick={() => onRemove(index)}>
          ×
        </ActionIcon>
      </Group>
      <MultiSelect
        size="xs"
        label={t("admin.lowcode.listViewFields")}
        data={fieldNames}
        value={row.fields}
        onChange={(value) => onUpdate(index, { fields: value })}
        searchable
      />
      <MultiSelect
        size="xs"
        label={t("admin.lowcode.listViewFilters")}
        data={fieldNames}
        value={row.filters}
        onChange={(value) => onUpdate(index, { filters: value })}
        searchable
      />
      <Group align="flex-end">
        <Select
          size="xs"
          label={t("admin.lowcode.listViewDefaultSort")}
          data={sortOptions}
          value={row.sortField}
          onChange={(value) => onUpdate(index, { sortField: value ?? "" })}
          allowDeselect={false}
          style={{ flex: 1 }}
        />
        <Checkbox
          label={t("admin.lowcode.listViewDescending")}
          checked={row.sortDesc}
          disabled={row.sortField.trim().length === 0}
          onChange={(e) => onUpdate(index, { sortDesc: e.currentTarget.checked })}
        />
        <NumberInput
          size="xs"
          label={t("admin.lowcode.listViewMaxLimit")}
          value={row.maxLimit}
          onChange={(value) =>
            onUpdate(index, { maxLimit: typeof value === "number" ? value : 50 })
          }
          min={1}
          max={200}
          w={110}
        />
      </Group>
    </Stack>
  );
});

function ListViewBuilder({
  listViews,
  fieldNames,
  onChange,
}: {
  listViews: ListViewRow[];
  fieldNames: string[];
  onChange: Dispatch<SetStateAction<ListViewRow[]>>;
}) {
  const { t } = useTranslation();
  const sortOptions = useMemo(
    () => [
      { value: "", label: t("admin.lowcode.noDefaultSort") },
      ...fieldNames.map((f) => ({ value: f, label: f })),
    ],
    [fieldNames, t],
  );

  const updateRow = useCallback(
    (index: number, patch: Partial<ListViewRow>) => {
      onChange((prev) => prev.map((row, i) => (i === index ? { ...row, ...patch } : row)));
    },
    [onChange],
  );

  const removeRow = useCallback(
    (index: number) => {
      onChange((prev) => prev.filter((_, i) => i !== index));
    },
    [onChange],
  );

  const addRow = useCallback(() => {
    onChange((prev) => [...prev, emptyListViewRow()]);
  }, [onChange]);

  return (
    <Stack gap="xs">
      <Text size="sm" fw={500}>
        {t("admin.lowcode.listViews")}
      </Text>
      {listViews.length === 0 ? (
        <Text size="sm" c="dimmed">
          {t("admin.lowcode.noListViews")}
        </Text>
      ) : null}
      {listViews.map((row, index) => (
        <ListViewRowEditor
          key={index}
          row={row}
          index={index}
          fieldNames={fieldNames}
          sortOptions={sortOptions}
          onUpdate={updateRow}
          onRemove={removeRow}
        />
      ))}
      <Group>
        <Button variant="default" size="xs" onClick={addRow}>
          {t("admin.lowcode.addListView")}
        </Button>
      </Group>
    </Stack>
  );
}

function LowCodeVersionHistory({
  name,
  onRollback,
}: {
  name: string;
  onRollback: (versionNumber: number) => void;
}) {
  const { t } = useTranslation();
  const { data: versions, isLoading, error } = useLowCodeVersions(name);

  if (isLoading) {
    return <Text size="sm">{t("common.loading")}</Text>;
  }
  if (error) {
    return <ApiErrorMessage error={error} />;
  }

  return (
    <Table>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>{t("admin.lowcode.versions.version")}</Table.Th>
          <Table.Th>{t("admin.lowcode.versions.publishedAt")}</Table.Th>
          <Table.Th>{t("admin.lowcode.versions.restoredFrom")}</Table.Th>
          <Table.Th>{t("common.actions")}</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {(versions ?? []).length === 0 ? (
          <Table.Tr>
            <Table.Td colSpan={4}>{t("common.noRecords")}</Table.Td>
          </Table.Tr>
        ) : (
          (versions as LowCodeVersionSummary[]).map((v) => (
            <Table.Tr key={v.versionNumber}>
              <Table.Td>{v.versionNumber}</Table.Td>
              <Table.Td>{v.publishedAt}</Table.Td>
              <Table.Td>{v.restoredFromVersion ?? "—"}</Table.Td>
              <Table.Td>
                <Button
                  variant="subtle"
                  size="compact-sm"
                  onClick={() => onRollback(v.versionNumber)}
                >
                  {t("admin.lowcode.versions.rollback")}
                </Button>
              </Table.Td>
            </Table.Tr>
          ))
        )}
      </Table.Tbody>
    </Table>
  );
}

export function LowCodeEntitiesAdminPage() {
  const { t } = useTranslation();
  const { data: entities, isLoading, error, refetch } = useLowCodeEntities();
  const { getDraft, saveDraft, publish, rollback } = useLowCodeActions();

  const [name, setName] = useState("");
  const [label, setLabel] = useState("");
  const [fields, setFields] = useState<FieldRow[]>([]);
  const [listViews, setListViews] = useState<ListViewRow[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [rowError, setRowError] = useState<string | null>(null);
  const [expandedName, setExpandedName] = useState<string | null>(null);

  function resetForm() {
    setName("");
    setLabel("");
    setFields([]);
    setListViews([]);
    setFormError(null);
  }

  async function handleLoad(targetName: string) {
    if (targetName.trim().length === 0) {
      return;
    }
    setFormError(null);
    try {
      const draft = await getDraft(targetName.trim());
      if (draft) {
        setLabel(draft.label);
        setFields(draft.fields.map(wireToFieldRow));
        setListViews(draft.listViews.map(wireToListViewRow));
      } else {
        setLabel("");
        setFields([]);
        setListViews([]);
      }
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  async function handleSaveDraft() {
    setFormError(null);

    if (fields.some((f) => f.name.trim().length === 0 || f.label.trim().length === 0)) {
      setFormError(t("admin.lowcode.fieldNameLabelRequired"));
      return;
    }
    if (listViews.some((v) => v.name.trim().length === 0 || v.label.trim().length === 0)) {
      setFormError(t("admin.lowcode.listViewNameLabelRequired"));
      return;
    }

    setSaving(true);
    try {
      await saveDraft(name.trim(), {
        label,
        fields: fields.map(fieldRowToWire),
        listViews: listViews.map(listViewRowToWire),
      });
      await refetch();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    } finally {
      setSaving(false);
    }
  }

  // `useMemo`'d so this array only gets a new reference when `fields` itself changes —
  // otherwise every keystroke/checkbox toggle anywhere on the page would hand `ListViewBuilder`
  // a brand-new `fieldNames` array, cascading into every `MultiSelect`'s `data` prop and
  // defeating `ListViewRowEditor`'s memoization.
  const fieldNames = useMemo(
    () => [...fields.map((f) => f.name.trim()).filter(Boolean), ...IMPLICIT_SYSTEM_FIELDS],
    [fields],
  );

  async function handlePublish(entityName: string) {
    setRowError(null);
    try {
      await publish(entityName);
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  async function handleRollback(entityName: string, versionNumber: number) {
    if (!window.confirm(t("admin.lowcode.versions.rollbackConfirm", { version: versionNumber }))) {
      return;
    }
    setRowError(null);
    try {
      await rollback(entityName, versionNumber);
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  const allNames = Array.from(
    new Set([...(entities?.published ?? []), ...(entities?.drafts ?? [])]),
  ).sort();

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {t("admin.lowcode.title")}
      </Title>

      <Stack mb="xl" maw={860}>
        <Title order={4}>{t("admin.lowcode.editTitle")}</Title>
        {formError ? <Alert color="red">{formError}</Alert> : null}
        <Group align="flex-end">
          <TextInput
            style={{ flex: 1 }}
            label={t("admin.lowcode.entityName")}
            description={t("admin.lowcode.entityNameDescription")}
            value={name}
            onChange={(event) => setName(event.currentTarget.value)}
          />
          <Button
            variant="default"
            onClick={() => void handleLoad(name)}
            disabled={name.trim().length === 0}
          >
            {t("admin.lowcode.load")}
          </Button>
        </Group>
        <TextInput
          label={t("admin.lowcode.label")}
          value={label}
          onChange={(event) => setLabel(event.currentTarget.value)}
        />
        <FieldBuilder fields={fields} onChange={setFields} />
        <ListViewBuilder listViews={listViews} fieldNames={fieldNames} onChange={setListViews} />
        <Group>
          <Button
            onClick={() => void handleSaveDraft()}
            loading={saving}
            disabled={name.trim().length === 0 || label.trim().length === 0}
          >
            {t("admin.lowcode.saveDraft")}
          </Button>
          <Button variant="subtle" onClick={resetForm}>
            {t("common.new")}
          </Button>
        </Group>
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
              <Table.Th>{t("admin.lowcode.entityName")}</Table.Th>
              <Table.Th>{t("admin.lowcode.status")}</Table.Th>
              <Table.Th>{t("common.actions")}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {allNames.length === 0 ? (
              <Table.Tr>
                <Table.Td colSpan={3}>{t("common.noRecords")}</Table.Td>
              </Table.Tr>
            ) : (
              allNames.map((entityName) => {
                const published = (entities?.published ?? []).includes(entityName);
                return (
                  <Fragment key={entityName}>
                    <Table.Tr>
                      <Table.Td>{entityName}</Table.Td>
                      <Table.Td>
                        <Badge color={published ? "green" : "gray"}>
                          {published ? t("admin.lowcode.published") : t("admin.lowcode.draftOnly")}
                        </Badge>
                      </Table.Td>
                      <Table.Td>
                        <Group gap="xs" wrap="nowrap">
                          <Button
                            variant="subtle"
                            size="compact-sm"
                            onClick={() => {
                              setName(entityName);
                              void handleLoad(entityName);
                            }}
                          >
                            {t("common.edit")}
                          </Button>
                          <Button
                            variant="subtle"
                            size="compact-sm"
                            onClick={() => void handlePublish(entityName)}
                          >
                            {t("admin.lowcode.publish")}
                          </Button>
                          <Button
                            variant="subtle"
                            size="compact-sm"
                            onClick={() =>
                              setExpandedName((current) =>
                                current === entityName ? null : entityName,
                              )
                            }
                          >
                            {expandedName === entityName
                              ? t("workflow.hide")
                              : t("admin.lowcode.versions.title")}
                          </Button>
                        </Group>
                      </Table.Td>
                    </Table.Tr>
                    {expandedName === entityName ? (
                      <Table.Tr>
                        <Table.Td colSpan={3}>
                          <LowCodeVersionHistory
                            name={entityName}
                            onRollback={(versionNumber) =>
                              void handleRollback(entityName, versionNumber)
                            }
                          />
                        </Table.Td>
                      </Table.Tr>
                    ) : null}
                  </Fragment>
                );
              })
            )}
          </Table.Tbody>
        </Table>
      )}
    </Container>
  );
}
