import { describe, expect, it } from "vitest";
import { describeDatabaseUrl } from "./client";

describe("describeDatabaseUrl", () => {
  it("returns host:port/database and never the username/password", () => {
    const description = describeDatabaseUrl(
      "postgres://metap:supersecret@localhost:5433/metap_test",
    );

    expect(description).toBe("localhost:5433/metap_test");
    expect(description).not.toContain("metap:supersecret");
    expect(description).not.toContain("supersecret");
  });

  it("defaults to port 5432 when the URL has no explicit port", () => {
    const description = describeDatabaseUrl("postgres://user:pass@db.internal/metap");

    expect(description).toBe("db.internal:5432/metap");
  });
});
