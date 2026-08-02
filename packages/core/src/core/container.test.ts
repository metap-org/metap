import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { createContainer } from "./container";
import type { AppConfig } from "../server/config";

const databaseUrl =
  process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";
const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

describe("createContainer outbox DB wiring", () => {
  let tmpDir: string;

  function baseConfig(overrides?: Partial<AppConfig>): AppConfig {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-container-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    return {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
      ...overrides,
    };
  }

  afterEach(() => {
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("reuses the same Database connection for outbox when outboxDatabaseUrl is not set", async () => {
    const container = createContainer(baseConfig());

    try {
      expect(container.outboxDb).toBe(container.db);
    } finally {
      await container.close();
    }
  });

  it("uses a separate Database connection for outbox when outboxDatabaseUrl is set, and closes both", async () => {
    const container = createContainer(baseConfig({ outboxDatabaseUrl: databaseUrl }));

    try {
      expect(container.outboxDb).not.toBe(container.db);
    } finally {
      await expect(container.close()).resolves.not.toThrow();
    }
  });
});
