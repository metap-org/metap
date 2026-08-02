import { Badge } from "@mantine/core";
import type { EntityField } from "../metadata/types";
import { formatFieldValue } from "./fieldKindConfig";

export function FieldValue({ field, value }: { field: EntityField; value: unknown }) {
  if (value === null || value === undefined) {
    return <>—</>;
  }

  const formatted = formatFieldValue(field.kind, value) ?? "—";

  if (field.kind === "enum") {
    return <Badge variant="light">{formatted}</Badge>;
  }

  return <>{formatted}</>;
}
