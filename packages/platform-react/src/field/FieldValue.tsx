import { Badge, Tooltip } from "@mantine/core";
import type { EntityField } from "../metadata/types";
import { formatFieldValue } from "./fieldKindConfig";
import { ReferenceFieldValue } from "./ReferenceFieldValue";

export function FieldValue({ field, value }: { field: EntityField; value: unknown }) {
  if (value === null || value === undefined) {
    if (field.required) {
      return (
        <Tooltip label="You don't have permission to view this field">
          <Badge variant="outline" color="gray">
            Masked
          </Badge>
        </Tooltip>
      );
    }
    return <>—</>;
  }

  if (field.kind === "reference") {
    return <ReferenceFieldValue field={field} value={value} />;
  }

  const formatted = formatFieldValue(field.kind, value) ?? "—";

  if (field.kind === "enum") {
    return <Badge variant="light">{formatted}</Badge>;
  }

  return <>{formatted}</>;
}
