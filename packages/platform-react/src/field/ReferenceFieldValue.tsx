import { useApiQuery } from "../api/useApiQuery";
import type { EntityField } from "../metadata/types";

type RecordDto = {
  id: string;
  code: string | null;
  status: string | null;
  version: number;
  data: Record<string, unknown>;
};

export function ReferenceFieldValue({ field, value }: { field: EntityField; value: unknown }) {
  const refEntity = field.refEntity;
  const id = typeof value === "string" ? value : undefined;

  const { data: record } = useApiQuery<{ data: RecordDto }, RecordDto>(
    ["record", refEntity, id],
    `/api/${refEntity}/${id}`,
    (response) => response.data,
    Boolean(refEntity && id),
  );

  if (!id) {
    return <>—</>;
  }

  const raw = record && field.refDisplayField ? record.data[field.refDisplayField] : undefined;
  return <>{typeof raw === "string" ? raw : id}</>;
}
