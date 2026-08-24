import { useState } from "react";
import { Select } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { useApiQuery } from "../api/useApiQuery";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

function labelFor(record: RecordDto, refDisplayField: string | undefined): string {
  const raw = refDisplayField ? record.data[refDisplayField] : undefined;
  return typeof raw === "string" ? raw : record.id;
}

export function ReferenceFieldInput({
  field,
  value,
  onChange,
  error,
  disabled,
}: {
  field: EntityField;
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
  disabled?: boolean;
}) {
  const label = field.label + (field.required ? " *" : "");
  const description = disabled ? "You can't edit this field" : undefined;
  const refEntity = field.refEntity;
  const currentValue = typeof value === "string" ? value : null;

  const [searchInput, setSearchInput] = useState("");
  const [debouncedSearch] = useDebouncedValue(searchInput, 300);

  const { data: currentRecord } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", refEntity, currentValue],
    `/api/${refEntity}/${currentValue}`,
    (response) => response.data,
    Boolean(refEntity && currentValue),
  );

  // No search text yet -> just the first page, unfiltered, so a small reference set (a handful
  // of projects, say) shows options immediately on open instead of looking empty/broken until
  // the caller types something (found live: the combobox for `jira.sprints.project` looked like
  // it wasn't loading anything at all). `?field=` (empty) would now mean "IS NULL" since
  // `metap-query`'s empty-filter-value fix, so this branch omits the param entirely rather than
  // sending it empty.
  const searchPath = debouncedSearch.length > 0
    ? `/api/${refEntity}?${field.refDisplayField}=${encodeURIComponent(debouncedSearch)}&limit=10`
    : `/api/${refEntity}?limit=10`;
  const { data: searchResults } = useApiQuery<{ data: RecordDto[] }, RecordDto[]>(
    ["reference-search", refEntity, field.refDisplayField, debouncedSearch],
    searchPath,
    (response) => response.data,
    Boolean(refEntity && field.refDisplayField),
  );

  const options = new Map<string, string>();
  if (currentRecord) {
    options.set(currentRecord.id, labelFor(currentRecord, field.refDisplayField));
  }
  for (const record of searchResults ?? []) {
    options.set(record.id, labelFor(record, field.refDisplayField));
  }

  return (
    <Select
      label={label}
      description={description}
      searchable
      data={[...options.entries()].map(([optionValue, optionLabel]) => ({
        value: optionValue,
        label: optionLabel,
      }))}
      value={currentValue}
      searchValue={searchInput}
      onSearchChange={setSearchInput}
      onChange={(selected) => onChange(selected ?? undefined)}
      error={error}
      disabled={disabled}
    />
  );
}
