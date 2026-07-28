import { defineConfig } from "drizzle-kit";

export default defineConfig({
  dialect: "postgresql",
  schema: "./src/infra/db/schema.ts",
  out: "./src/infra/db/migrations",
  dbCredentials: {
    url: process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5432/metap",
  },
});
