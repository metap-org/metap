import "dotenv/config";
import { z } from "zod";

const ConfigSchema = z.object({
  nodeEnv: z.enum(["development", "test", "production"]).default("development"),
  host: z.string().default("0.0.0.0"),
  port: z.coerce.number().int().positive().default(3000),
  databaseUrl: z.string().url(),
  rabbitmqUrl: z.string().url(),
  corsOrigins: z.array(z.string().url()).default([]),
  authJwtPublicKeyPath: z.string().min(1, "AUTH_JWT_PUBLIC_KEY_PATH is required"),
});

export type AppConfig = z.infer<typeof ConfigSchema>;

export function loadConfig(): AppConfig {
  return ConfigSchema.parse({
    nodeEnv: process.env.NODE_ENV,
    host: process.env.HOST,
    port: process.env.PORT,
    databaseUrl: process.env.DATABASE_URL,
    rabbitmqUrl: process.env.RABBITMQ_URL,
    corsOrigins: process.env.CORS_ORIGINS?.split(",").filter(Boolean),
    authJwtPublicKeyPath: process.env.AUTH_JWT_PUBLIC_KEY_PATH,
  });
}
