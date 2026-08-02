import { and, eq } from "drizzle-orm";
import type { Database } from "../../infra/db/client";
import { userRoles } from "../../infra/db/schema";

export class RoleAssignmentService {
  constructor(private readonly db: Database) {}

  async getRolesForUser(tenantId: string, userId: string): Promise<string[]> {
    const rows = await this.db.client
      .select({ role: userRoles.role })
      .from(userRoles)
      .where(and(eq(userRoles.tenantId, tenantId), eq(userRoles.userId, userId)));

    return rows.map((row) => row.role);
  }

  async assignRole(
    tenantId: string,
    userId: string,
    role: string,
    assignedBy: string | undefined,
  ): Promise<void> {
    await this.db.client
      .insert(userRoles)
      .values({ tenantId, userId, role, createdBy: assignedBy })
      .onConflictDoNothing({
        target: [userRoles.tenantId, userRoles.userId, userRoles.role],
      });
  }

  async revokeRole(tenantId: string, userId: string, role: string): Promise<void> {
    await this.db.client
      .delete(userRoles)
      .where(
        and(
          eq(userRoles.tenantId, tenantId),
          eq(userRoles.userId, userId),
          eq(userRoles.role, role),
        ),
      );
  }

  async listUsers(tenantId: string): Promise<{ userId: string; roles: string[] }[]> {
    const rows = await this.db.client
      .select({ userId: userRoles.userId, role: userRoles.role })
      .from(userRoles)
      .where(eq(userRoles.tenantId, tenantId));

    const grouped = new Map<string, string[]>();
    for (const row of rows) {
      const roles = grouped.get(row.userId) ?? [];
      roles.push(row.role);
      grouped.set(row.userId, roles);
    }

    return [...grouped.entries()].map(([userId, roles]) => ({ userId, roles }));
  }
}
