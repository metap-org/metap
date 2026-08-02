import "dotenv/config";
import { Client } from "pg";

const tenantId = process.argv[2];
const userId = process.argv[3];

if (!tenantId || !userId) {
  console.error("Usage: pnpm seed:admin <tenantId> <userId>");
  process.exit(1);
}

const client = new Client({ connectionString: process.env.DATABASE_URL });
await client.connect();

try {
  await client.query(
    `INSERT INTO user_roles (tenant_id, user_id, role)
     VALUES ($1, $2, 'admin')
     ON CONFLICT (tenant_id, user_id, role) DO NOTHING`,
    [tenantId, userId],
  );
  console.log(`Granted 'admin' role to user ${userId} in tenant ${tenantId}.`);
} finally {
  await client.end();
}
