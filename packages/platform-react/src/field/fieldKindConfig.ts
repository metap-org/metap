import type { FieldKind } from "../metadata/types";

const numberFormatter = new Intl.NumberFormat();
const dateFormatter = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });
const dateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

// value is `unknown` (it's whatever came back in a record's JSONB `data`
// blob) — String(unknown) risks "[object Object]" for a non-primitive, so
// every fallback here narrows explicitly instead of calling String() on an
// untyped value.
function safeString(value: unknown): string {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return JSON.stringify(value);
}

export function formatFieldValue(kind: FieldKind, value: unknown): string | null {
  if (value === null || value === undefined) {
    return null;
  }

  switch (kind) {
    case "number":
    case "money":
      return typeof value === "number" ? numberFormatter.format(value) : safeString(value);
    case "boolean":
      return value ? "Yes" : "No";
    case "date":
      return typeof value === "string" ? dateFormatter.format(new Date(value)) : safeString(value);
    case "datetime":
      return typeof value === "string"
        ? dateTimeFormatter.format(new Date(value))
        : safeString(value);
    case "json":
      return JSON.stringify(value);
    case "id":
    case "string":
    case "reference":
    case "enum":
      return safeString(value);
  }
}
