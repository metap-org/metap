import { Checkbox, NumberInput, Select, Textarea, TextInput } from "@mantine/core";
import { DateInput, DateTimePicker } from "@mantine/dates";
import type { EntityField } from "../metadata/types";
import { ReferenceFieldInput } from "./ReferenceFieldInput";

export function FieldInput({
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

  switch (field.kind) {
    case "id":
      return null;
    case "boolean":
      return (
        <Checkbox
          label={label}
          description={description}
          checked={Boolean(value)}
          onChange={(event) => onChange(event.currentTarget.checked)}
          error={error}
          disabled={disabled}
        />
      );
    case "number":
    case "money":
      return (
        <NumberInput
          label={label}
          description={description}
          value={typeof value === "number" ? value : ""}
          onChange={(v) => onChange(typeof v === "number" ? v : undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "date":
      return (
        <DateInput
          label={label}
          description={description}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "datetime":
      return (
        <DateTimePicker
          label={label}
          description={description}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "enum":
      return (
        <Select
          label={label}
          description={description}
          data={(field.enumValues ?? []).map((v) => ({ value: v, label: v }))}
          value={typeof value === "string" ? value : null}
          onChange={(v) => onChange(v ?? undefined)}
          error={error}
          disabled={disabled}
        />
      );
    case "json":
      return (
        <Textarea
          label={label}
          description={description}
          value={typeof value === "string" ? value : value ? JSON.stringify(value) : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
          disabled={disabled}
        />
      );
    case "string":
      return (
        <TextInput
          label={label}
          description={description}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.currentTarget.value)}
          error={error}
          disabled={disabled}
        />
      );
    case "reference":
      return (
        <ReferenceFieldInput
          field={field}
          value={value}
          onChange={onChange}
          error={error}
          disabled={disabled}
        />
      );
  }
}
