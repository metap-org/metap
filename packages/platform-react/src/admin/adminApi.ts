import { useQueryClient } from "@tanstack/react-query";
import { useApiMutation } from "../api/useApiMutation";
import { useApiQuery } from "../api/useApiQuery";
import { useAuth } from "../auth/AuthContext";
import { apiFetch } from "../api/client";

export type AdminUser = { userId: string; roles: string[] };

export type AdminPolicy = {
  id: string;
  tenantId: string;
  entity: string;
  action: string;
  field: string | null;
  subject: string;
  roles: string[] | null;
  condition: unknown;
  createdBy: string | null;
};

export type CronJob = {
  id: string;
  tenantId: string;
  name: string;
  enabled: boolean;
  cronExpr: string;
  timezone: string;
  targetType: string;
  targetConfig: unknown;
  dispatchMode: string;
  nextRunAt: string;
  lastRunAt: string | null;
  createdAt: string;
  updatedAt: string;
  createdBy: string | null;
};

export type CronJobRun = {
  id: string;
  tenantId: string;
  jobId: string;
  status: string;
  scheduledFor: string;
  startedAt: string | null;
  finishedAt: string | null;
  error: string | null;
  responseSummary: unknown;
  createdAt: string;
};

// --- Users ---

export function useAdminUsers() {
  return useApiQuery<{ data: AdminUser[] }, AdminUser[]>(
    ["admin", "users"],
    "/admin/users",
    (response) => response.data,
  );
}

export function useCreateAdminUser() {
  return useApiMutation<
    { data: { userId: string; email: string; roles: string[] } },
    { email: string; password: string; roles: string[] }
  >("POST", "/admin/users");
}

/** Row-level actions (assign/revoke role) need a per-user path, which `useApiMutation`'s
 * fixed-path shape can't express — same convention as `GeneratedList`'s per-row delete: a
 * plain `apiFetch` call plus manual invalidation instead of a bound mutation hook. */
export function useAdminRoleActions() {
  const { token } = useAuth();
  const queryClient = useQueryClient();

  async function assignRole(userId: string, role: string) {
    await apiFetch(`/admin/users/${userId}/roles`, token, {
      method: "POST",
      body: JSON.stringify({ role }),
    });
    await queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
  }

  async function revokeRole(userId: string, role: string) {
    await apiFetch(`/admin/users/${userId}/roles/${role}`, token, { method: "DELETE" });
    await queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
  }

  return { assignRole, revokeRole };
}

// --- Policies ---

export function useAdminPolicies(entity?: string) {
  const path = entity ? `/admin/policies?entity=${encodeURIComponent(entity)}` : "/admin/policies";
  return useApiQuery<{ data: AdminPolicy[] }, AdminPolicy[]>(
    ["admin", "policies", entity ?? null],
    path,
    (response) => response.data,
  );
}

export function useCreateAdminPolicy() {
  return useApiMutation<
    { data: AdminPolicy },
    {
      entity: string;
      action: string;
      roles?: string[];
      condition?: unknown;
      field?: string;
      subject?: string;
    }
  >("POST", "/admin/policies");
}

export function useDeleteAdminPolicy() {
  const { token } = useAuth();
  const queryClient = useQueryClient();

  return async function deletePolicy(id: string) {
    await apiFetch(`/admin/policies/${id}`, token, { method: "DELETE" });
    await queryClient.invalidateQueries({ queryKey: ["admin", "policies"] });
  };
}

// --- Cron jobs ---

export function useAdminCronJobs() {
  return useApiQuery<{ data: CronJob[] }, CronJob[]>(
    ["admin", "cronJobs"],
    "/admin/cron-jobs",
    (response) => response.data,
  );
}

export function useCronJobRuns(jobId: string | null) {
  return useApiQuery<{ data: CronJobRun[] }, CronJobRun[]>(
    ["admin", "cronJobs", jobId, "runs"],
    `/admin/cron-jobs/${jobId}/runs`,
    (response) => response.data,
    jobId !== null,
  );
}

export function useCreateAdminCronJob() {
  return useApiMutation<
    { data: CronJob },
    {
      name: string;
      cronExpr: string;
      timezone: string;
      targetType: string;
      targetConfig: unknown;
      dispatchMode: string;
      enabled: boolean;
    }
  >("POST", "/admin/cron-jobs");
}

/** Row-level actions (update/delete) need a per-job path — see `useAdminRoleActions`'s doc
 * comment for why this bypasses `useApiMutation`. */
export function useAdminCronJobActions() {
  const { token } = useAuth();
  const queryClient = useQueryClient();

  async function toggleEnabled(job: CronJob) {
    await apiFetch(`/admin/cron-jobs/${job.id}`, token, {
      method: "PATCH",
      body: JSON.stringify({ enabled: !job.enabled }),
    });
    await queryClient.invalidateQueries({ queryKey: ["admin", "cronJobs"] });
  }

  async function deleteJob(id: string) {
    await apiFetch(`/admin/cron-jobs/${id}`, token, { method: "DELETE" });
    await queryClient.invalidateQueries({ queryKey: ["admin", "cronJobs"] });
  }

  return { toggleEnabled, deleteJob };
}
