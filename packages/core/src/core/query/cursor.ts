export type Cursor = {
  field: string;
  value: string;
  id: string;
  dir: "asc" | "desc";
};

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function encodeCursor(cursor: Cursor): string {
  return Buffer.from(JSON.stringify(cursor), "utf8").toString("base64");
}

export function decodeCursor(raw: string): Cursor | undefined {
  let parsed: unknown;

  try {
    parsed = JSON.parse(Buffer.from(raw, "base64").toString("utf8"));
  } catch {
    return undefined;
  }

  if (typeof parsed !== "object" || parsed === null) {
    return undefined;
  }

  const candidate = parsed as Partial<Cursor>;

  if (
    typeof candidate.field !== "string" ||
    typeof candidate.value !== "string" ||
    typeof candidate.id !== "string" ||
    !UUID_RE.test(candidate.id) ||
    (candidate.dir !== "asc" && candidate.dir !== "desc")
  ) {
    return undefined;
  }

  return { field: candidate.field, value: candidate.value, id: candidate.id, dir: candidate.dir };
}
