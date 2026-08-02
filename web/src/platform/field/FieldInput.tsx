import { Checkbox, NumberInput, Select, Textarea, TextInput } from "@mantine/core";
import { DateInput, DateTimePicker } from "@mantine/dates";
import type { EntityField } from "../metadata/types";

export function FieldInput({
  field,
  value,
  onChange,
  error,
}: {
  field: EntityField;
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
}) {
  const label = field.label + (field.required ? " *" : "");

  switch (field.kind) {
    case "id":
      return null;
    case "boolean":
      return (
        <Checkbox
          label={label}
          checked={Boolean(value)}
          onChange={(event) => onChange(event.currentTarget.checked)}
          error={error}
        />
      );
    case "number":
    case "money":
      return (
        <NumberInput
          label={label}
          value={typeof value === "number" ? value : ""}
          onChange={(v) => onChange(typeof v === "number" ? v : undefined)}
          error={error}
        />
      );
    case "date":
      return (
        <DateInput
          label={label}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
        />
      );
    case "datetime":
      return (
        <DateTimePicker
          label={label}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
        />
      );
    case "enum":
      return (
        <Select
          label={label}
          data={(field.enumValues ?? []).map((v) => ({ value: v, label: v }))}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
        />
      );
    case "json":
      return (
        <Textarea
          label={label}
          value={typeof value === "string" ? value : value ? JSON.stringify(value) : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
        />
      );
    case "string":
    case "reference":
      return (
        <TextInput
          label={label}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
        />
      );
  }
}
