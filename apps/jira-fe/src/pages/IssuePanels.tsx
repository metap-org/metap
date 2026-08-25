import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Badge, Button, Card, Group, Select, Stack, Text, TextInput, Title } from "@mantine/core";
import { apiFetch, useApiQuery, useAuth, useCurrentUser } from "@metap/platform-react";
import type { ListResponse } from "../api/types";

type TenantUser = { id: string; email: string };
type UsersResponse = { data: TenantUser[] };

/** `GET /users` (`crates/metap-http/src/routes/users.rs`) — every user in the tenant, the "pick
 *  a user" primitive an assignee/reporter/watcher picker needs. Not `Reference`-backed: `users`
 *  is a platform/auth table, not a registered `EntityDefinition` (see `issue_entity.rs`'s doc
 *  comment), so this is a bespoke picker, not `ReferenceFieldInput`. */
function useTenantUsers() {
  const { data } = useApiQuery<UsersResponse, TenantUser[]>(
    ["tenant-users"],
    "/users",
    (r) => r.data,
  );
  return data ?? [];
}

/** The logged-in caller's own email — the JWT only carries `sub` (a user id), never email
 *  (`crates/metap-http/src/auth.rs`'s `Claims`), so this cross-references `GET /auth/me`'s
 *  `userId` against the tenant user list rather than needing a new backend field. */
function useCurrentUserEmail(): string | null {
  const { data: me } = useCurrentUser();
  const users = useTenantUsers();
  return users.find((u) => u.id === me?.userId)?.email ?? null;
}

/**
 * Real picker for `assigneeEmail` — plain `String` field on `jira.issues`, not a `Reference`
 * (nothing to reference: `users` isn't an `EntityDefinition`). The generic edit form still shows
 * it as free text (`FieldInput` dispatches purely by `FieldKind`, no per-field override
 * mechanism exists); this widget is the real picker, PATCH-ing the field directly.
 */
export function AssigneePicker({
  issueId,
  currentEmail,
}: {
  issueId: string;
  currentEmail: string | null;
}) {
  const { token } = useAuth();
  const queryClient = useQueryClient();
  const users = useTenantUsers();
  const [saving, setSaving] = useState(false);

  async function handleChange(email: string | null) {
    setSaving(true);
    try {
      const record = await apiFetch<{ data: { version: number } }>(
        `/api/jira.issues/${issueId}`,
        token,
      );
      await apiFetch(`/api/jira.issues/${issueId}`, token, {
        method: "PATCH",
        body: JSON.stringify({
          version: record.data.version,
          data: { assigneeEmail: email ?? undefined },
        }),
      });
      await queryClient.invalidateQueries({ queryKey: ["record", "jira.issues", issueId] });
    } finally {
      setSaving(false);
    }
  }

  return (
    <Select
      label="Assignee"
      placeholder="Unassigned"
      data={users.map((u) => ({ value: u.email, label: u.email }))}
      value={currentEmail}
      onChange={(value) => void handleChange(value)}
      disabled={saving}
      clearable
    />
  );
}

export function formatMinutes(minutes: number): string {
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return hours > 0 ? `${hours}h ${rest}m` : `${rest}m`;
}

type WorklogRecord = {
  id: string;
  data: { authorEmail: string; timeSpentMinutes: number; workDate: string; description?: string };
};

/**
 * `jira.worklogs` — time tracking entries ("logwork"), compared against `jira.issues`'s
 * `originalEstimateMinutes` for an estimate-vs-logged total, the same pair real Jira tracks.
 */
export function WorklogsPanel({
  issueId,
  originalEstimateMinutes,
}: {
  issueId: string;
  originalEstimateMinutes: number | null;
}) {
  const { token } = useAuth();
  const queryClient = useQueryClient();
  const myEmail = useCurrentUserEmail();
  const [minutes, setMinutes] = useState("");
  const [workDate, setWorkDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [description, setDescription] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const worklogsQueryKey = ["worklogs", issueId];
  const { data: worklogs } = useApiQuery<ListResponse<WorklogRecord>, WorklogRecord[]>(
    worklogsQueryKey,
    `/api/jira.worklogs?issue=${issueId}`,
    (response) => response.data,
  );

  const totalMinutes = (worklogs ?? []).reduce((sum, w) => sum + w.data.timeSpentMinutes, 0);

  async function handleSubmit() {
    if (!myEmail) return;
    setSubmitting(true);
    try {
      await apiFetch("/api/jira.worklogs", token, {
        method: "POST",
        body: JSON.stringify({
          data: {
            issue: issueId,
            authorEmail: myEmail,
            timeSpentMinutes: Number(minutes),
            workDate,
            description: description || undefined,
          },
        }),
      });
      setMinutes("");
      setDescription("");
      await queryClient.invalidateQueries({ queryKey: worklogsQueryKey });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Card withBorder mt="md" padding="md">
      <Title order={4} mb="sm">
        Work log
      </Title>

      <Group mb="sm" gap="xl">
        <Text size="sm">
          Logged: <strong>{formatMinutes(totalMinutes)}</strong>
        </Text>
        {originalEstimateMinutes ? (
          <Text size="sm">
            Estimate: <strong>{formatMinutes(originalEstimateMinutes)}</strong>
            {" — "}
            {totalMinutes > originalEstimateMinutes ? (
              <Text component="span" c="red">
                over by {formatMinutes(totalMinutes - originalEstimateMinutes)}
              </Text>
            ) : (
              <Text component="span" c="dimmed">
                {formatMinutes(originalEstimateMinutes - totalMinutes)} remaining
              </Text>
            )}
          </Text>
        ) : null}
      </Group>

      <Stack gap="xs" mb="md">
        {(worklogs ?? []).map((w) => (
          <Group key={w.id} justify="space-between">
            <Text size="sm">
              {w.data.authorEmail} — {formatMinutes(w.data.timeSpentMinutes)} on {w.data.workDate}
              {w.data.description ? ` — ${w.data.description}` : ""}
            </Text>
          </Group>
        ))}
        {worklogs?.length === 0 ? (
          <Text size="sm" c="dimmed">
            No work logged yet.
          </Text>
        ) : null}
      </Stack>

      <Group align="flex-end" gap="xs">
        <TextInput
          label="Minutes"
          type="number"
          w={100}
          value={minutes}
          onChange={(event) => setMinutes(event.currentTarget.value)}
        />
        <TextInput
          label="Date"
          type="date"
          value={workDate}
          onChange={(event) => setWorkDate(event.currentTarget.value)}
        />
        <TextInput
          label="Description"
          value={description}
          onChange={(event) => setDescription(event.currentTarget.value)}
        />
        <Button
          onClick={() => void handleSubmit()}
          loading={submitting}
          disabled={!myEmail || !minutes || Number(minutes) <= 0}
        >
          Log work
        </Button>
      </Group>
      {!myEmail ? (
        <Text size="xs" c="dimmed" mt={4}>
          Can't determine your email yet — reload if this persists.
        </Text>
      ) : null}
    </Card>
  );
}

type WatcherRecord = { id: string; version: number; data: { issue: string; userEmail: string } };

/**
 * Subscription list only — no notification *delivery* on top of it (`notification-worker` logs
 * transitions to stdout only, no email/webhook integration exists anywhere in this repo yet).
 * "Watch"/"Unwatch" toggles a `jira.watchers` row for the caller's own email.
 */
export function WatchersPanel({ issueId }: { issueId: string }) {
  const { token } = useAuth();
  const queryClient = useQueryClient();
  const myEmail = useCurrentUserEmail();
  const [busy, setBusy] = useState(false);

  const watchersQueryKey = ["watchers", issueId];
  const { data: watchers } = useApiQuery<ListResponse<WatcherRecord>, WatcherRecord[]>(
    watchersQueryKey,
    `/api/jira.watchers?issue=${issueId}`,
    (response) => response.data,
  );

  const myWatch = (watchers ?? []).find((w) => w.data.userEmail === myEmail);

  async function toggleWatch() {
    if (!myEmail) return;
    setBusy(true);
    try {
      if (myWatch) {
        await apiFetch(`/api/jira.watchers/${myWatch.id}`, token, {
          method: "DELETE",
          body: JSON.stringify({ version: myWatch.version }),
        });
      } else {
        await apiFetch("/api/jira.watchers", token, {
          method: "POST",
          body: JSON.stringify({ data: { issue: issueId, userEmail: myEmail } }),
        });
      }
      await queryClient.invalidateQueries({ queryKey: watchersQueryKey });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card withBorder mt="md" padding="md">
      <Group justify="space-between" mb="sm">
        <Title order={4}>Watchers ({watchers?.length ?? 0})</Title>
        <Button
          size="compact-sm"
          variant={myWatch ? "filled" : "outline"}
          loading={busy}
          onClick={() => void toggleWatch()}
        >
          {myWatch ? "Unwatch" : "Watch"}
        </Button>
      </Group>
      <Group gap="xs">
        {(watchers ?? []).map((w) => (
          <Badge key={w.id} variant="light">
            {w.data.userEmail}
          </Badge>
        ))}
      </Group>
    </Card>
  );
}
