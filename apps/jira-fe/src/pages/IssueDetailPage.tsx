import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import {
  Anchor,
  Badge,
  Button,
  Card,
  Container,
  Group,
  Stack,
  Text,
  Textarea,
  TextInput,
  Title,
} from "@mantine/core";
import {
  ApiError,
  ApiErrorMessage,
  RecordDetail,
  apiFetch,
  useApiMutation,
  useApiQuery,
  useAuth,
} from "@metap/platform-react";
import type {
  AttachmentRecord,
  CommentRecord,
  IssueLinkRecord,
  IssueRecord,
  ListResponse,
} from "../api/types";
import { AssigneePicker, WatchersPanel, WorklogsPanel } from "./IssuePanels";

/**
 * `parentIssue` is a self-referencing `Reference` field on `jira.issues` (`issue_entity.rs`) —
 * sub-tasks. Its own `filters: ["parentIssue"]` list_view entry means `?parentIssue={id}` already
 * works, same as `jira.comments`'s `?issue={id}`. Read-only here — creating a sub-task is just a
 * normal issue create with `parentIssue` set, already reachable through the generic "New issue"
 * form (its `Reference` picker now loads an initial page on open, not just on search — see
 * `ReferenceFieldInput`'s fix earlier this phase).
 */
function SubtasksPanel({ issueId }: { issueId: string }) {
  const {
    data: subtasks,
    isLoading,
    error,
  } = useApiQuery<ListResponse<IssueRecord>, IssueRecord[]>(
    ["subtasks", issueId],
    `/api/jira.issues?parentIssue=${issueId}`,
    (response) => response.data,
  );

  return (
    <Card withBorder mt="md" padding="md">
      <Title order={4} mb="sm">
        Sub-tasks
      </Title>

      {isLoading ? <Text size="sm">Loading…</Text> : null}
      {error ? <ApiErrorMessage error={error} /> : null}

      <Stack gap="xs">
        {(subtasks ?? []).map((subtask) => (
          <Group key={subtask.id} justify="space-between">
            <Anchor component={Link} to={`/issues/${subtask.id}`}>
              {subtask.data.title}
            </Anchor>
            <Badge size="sm" variant="light">
              {subtask.status}
            </Badge>
          </Group>
        ))}
        {subtasks?.length === 0 ? (
          <Text size="sm" c="dimmed">
            No sub-tasks yet — create an issue and set its Parent Issue to this one.
          </Text>
        ) : null}
      </Stack>
    </Card>
  );
}

/**
 * `jira.comments` has a `filters: ["issue"]` list_view (`comment_entity.rs`) — filtering by
 * `issue` already works through the generic `QueryPlanner`, so this panel needed no backend
 * change, only the UI `RecordDetail`'s generic field renderer doesn't have (no "related records"
 * concept there).
 */
function CommentsPanel({ issueId }: { issueId: string }) {
  const { token } = useAuth();
  const queryClient = useQueryClient();
  const [authorEmail, setAuthorEmail] = useState("");
  const [body, setBody] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editBody, setEditBody] = useState("");
  const [busyId, setBusyId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const commentsQueryKey = ["comments", issueId];
  const {
    data: comments,
    isLoading,
    error,
  } = useApiQuery<ListResponse<CommentRecord>, CommentRecord[]>(
    commentsQueryKey,
    `/api/jira.comments?issue=${issueId}&sort=-createdAt`,
    (response) => response.data,
  );

  const addComment = useApiMutation<
    { data: CommentRecord },
    { data: { issue: string; authorEmail: string; body: string } }
  >("POST", "/api/jira.comments");

  async function handleSubmit() {
    await addComment.mutateAsync({ data: { issue: issueId, authorEmail, body } });
    setBody("");
    void queryClient.invalidateQueries({ queryKey: commentsQueryKey });
  }

  function startEdit(comment: CommentRecord) {
    setActionError(null);
    setEditingId(comment.id);
    setEditBody(comment.data.body);
  }

  // `jira.comments` is an ordinary entity — update/delete already work generically through
  // CrudService, no backend change needed. Not `useApiMutation` (its `path` is fixed at hook
  // creation, but each row here needs its own `/api/jira.comments/{id}`) — plain `apiFetch`,
  // same as `RecordDetail`'s own delete handler already does.
  async function saveEdit(comment: CommentRecord) {
    setActionError(null);
    setBusyId(comment.id);
    try {
      await apiFetch(`/api/jira.comments/${comment.id}`, token, {
        method: "PATCH",
        body: JSON.stringify({ version: comment.version, data: { body: editBody } }),
      });
      setEditingId(null);
      await queryClient.invalidateQueries({ queryKey: commentsQueryKey });
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : "Failed to save comment.");
    } finally {
      setBusyId(null);
    }
  }

  async function deleteComment(comment: CommentRecord) {
    if (!window.confirm("Delete this comment?")) return;
    setActionError(null);
    setBusyId(comment.id);
    try {
      await apiFetch(`/api/jira.comments/${comment.id}`, token, {
        method: "DELETE",
        body: JSON.stringify({ version: comment.version }),
      });
      await queryClient.invalidateQueries({ queryKey: commentsQueryKey });
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : "Failed to delete comment.");
    } finally {
      setBusyId(null);
    }
  }

  return (
    <Card withBorder mt="md" padding="md">
      <Title order={4} mb="sm">
        Comments
      </Title>

      {isLoading ? <Text size="sm">Loading…</Text> : null}
      {error ? <ApiErrorMessage error={error} /> : null}
      {actionError ? (
        <Text size="sm" c="red" mb="xs">
          {actionError}
        </Text>
      ) : null}

      <Stack gap="sm" mb="md">
        {(comments ?? []).map((comment) => (
          <Card key={comment.id} withBorder padding="sm" bg="var(--mantine-color-gray-0)">
            <Group justify="space-between" mb={4}>
              <Text size="sm" fw={600}>
                {comment.data.authorEmail}
              </Text>
              <Group gap={4}>
                <Button size="compact-xs" variant="subtle" onClick={() => startEdit(comment)}>
                  Edit
                </Button>
                <Button
                  size="compact-xs"
                  variant="subtle"
                  color="red"
                  loading={busyId === comment.id}
                  onClick={() => void deleteComment(comment)}
                >
                  Delete
                </Button>
              </Group>
            </Group>
            {editingId === comment.id ? (
              <Stack gap="xs">
                <Textarea
                  autosize
                  minRows={2}
                  value={editBody}
                  onChange={(event) => setEditBody(event.currentTarget.value)}
                />
                <Group gap="xs">
                  <Button
                    size="compact-xs"
                    loading={busyId === comment.id}
                    onClick={() => void saveEdit(comment)}
                  >
                    Save
                  </Button>
                  <Button size="compact-xs" variant="subtle" onClick={() => setEditingId(null)}>
                    Cancel
                  </Button>
                </Group>
              </Stack>
            ) : (
              <Text size="sm">{comment.data.body}</Text>
            )}
          </Card>
        ))}
        {comments?.length === 0 ? (
          <Text size="sm" c="dimmed">
            No comments yet.
          </Text>
        ) : null}
      </Stack>

      <Stack gap="xs">
        <TextInput
          label="Your email"
          value={authorEmail}
          onChange={(event) => setAuthorEmail(event.currentTarget.value)}
        />
        <Textarea
          label="Comment"
          autosize
          minRows={2}
          value={body}
          onChange={(event) => setBody(event.currentTarget.value)}
        />
        <Button
          onClick={() => void handleSubmit()}
          loading={addComment.isPending}
          disabled={authorEmail.trim().length === 0 || body.trim().length === 0}
        >
          Add comment
        </Button>
        {addComment.error ? <ApiErrorMessage error={addComment.error} /> : null}
      </Stack>
    </Card>
  );
}

const LINK_LABEL: Record<string, string> = {
  relates_to: "relates to",
  blocks: "blocks",
  duplicates: "duplicates",
};
const LINK_LABEL_REVERSE: Record<string, string> = {
  relates_to: "relates to",
  blocks: "is blocked by",
  duplicates: "is duplicated by",
};

/**
 * `jira.issue_links` has **two** `Reference` fields both pointing at `jira.issues`
 * (`fromIssue`/`toIssue`) — a typed, symmetric relation distinct from `parentIssue`'s hierarchy.
 * This issue can be on either side of a link, so both directions are queried and shown with the
 * label read from the other side's perspective (`blocks` vs `is blocked by`).
 */
function IssueLinksPanel({ issueId }: { issueId: string }) {
  const { data: outgoing } = useApiQuery<ListResponse<IssueLinkRecord>, IssueLinkRecord[]>(
    ["issue-links", "from", issueId],
    `/api/jira.issue_links?fromIssue=${issueId}`,
    (response) => response.data,
  );
  const { data: incoming } = useApiQuery<ListResponse<IssueLinkRecord>, IssueLinkRecord[]>(
    ["issue-links", "to", issueId],
    `/api/jira.issue_links?toIssue=${issueId}`,
    (response) => response.data,
  );

  const rows = [
    ...(outgoing ?? []).map((link) => ({
      link,
      label: LINK_LABEL[link.data.linkType] ?? link.data.linkType,
      otherIssueId: link.data.toIssue,
      otherTitle: link.relatedDisplay?.toIssue ?? link.data.toIssue,
    })),
    ...(incoming ?? []).map((link) => ({
      link,
      label: LINK_LABEL_REVERSE[link.data.linkType] ?? link.data.linkType,
      otherIssueId: link.data.fromIssue,
      otherTitle: link.relatedDisplay?.fromIssue ?? link.data.fromIssue,
    })),
  ];

  if (rows.length === 0) return null;

  return (
    <Card withBorder mt="md" padding="md">
      <Title order={4} mb="sm">
        Linked issues
      </Title>
      <Stack gap="xs">
        {rows.map(({ link, label, otherIssueId, otherTitle }) => (
          <Group key={link.id} gap="xs">
            <Badge size="sm" variant="outline">
              {label}
            </Badge>
            <Anchor component={Link} to={`/issues/${otherIssueId}`}>
              {otherTitle}
            </Anchor>
          </Group>
        ))}
      </Stack>
    </Card>
  );
}

function formatSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KB`;
}

type AttachmentListResponse = { data: AttachmentRecord[] };

/**
 * `metap-storage::ObjectStore`'s first real consumer anywhere in this repo — generic
 * `/api/{entity}/{id}/attachments*` routes (`crates/metap-http/src/routes/attachments.rs`), not
 * jira-specific (any entity in any app gets this for free). Upload is `multipart/form-data`,
 * download needs an `Authorization` header a plain `<a href>` can't send, so both go through raw
 * `fetch` here rather than `apiFetch`/`useApiMutation` (which always set
 * `Content-Type: application/json` whenever a body is present — wrong for a multipart upload).
 */
function AttachmentsPanel({ issueId }: { issueId: string }) {
  const { token } = useAuth();
  const queryClient = useQueryClient();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);

  const attachmentsQueryKey = ["attachments", issueId];
  const {
    data: attachments,
    isLoading,
    error,
  } = useApiQuery<AttachmentListResponse, AttachmentRecord[]>(
    attachmentsQueryKey,
    `/api/jira.issues/${issueId}/attachments`,
    (response) => response.data,
  );

  async function handleFileSelected(file: File) {
    setUploadError(null);
    setUploading(true);
    try {
      const formData = new FormData();
      formData.append("file", file);
      const response = await fetch(`/api/jira.issues/${issueId}/attachments`, {
        method: "POST",
        headers: token ? { Authorization: `Bearer ${token}` } : undefined,
        body: formData,
      });
      if (!response.ok) {
        throw new Error(`upload failed with status ${response.status}`);
      }
      await queryClient.invalidateQueries({ queryKey: attachmentsQueryKey });
    } catch {
      setUploadError("Upload failed — please try again.");
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  }

  async function handleDownload(attachment: AttachmentRecord) {
    const response = await fetch(
      `/api/jira.issues/${issueId}/attachments/${attachment.id}/download`,
      {
        headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      },
    );
    if (!response.ok) return;
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = attachment.filename;
    link.click();
    URL.revokeObjectURL(url);
  }

  return (
    <Card withBorder mt="md" padding="md">
      <Title order={4} mb="sm">
        Attachments
      </Title>

      {isLoading ? <Text size="sm">Loading…</Text> : null}
      {error ? <ApiErrorMessage error={error} /> : null}

      <Stack gap="xs" mb="md">
        {(attachments ?? []).map((attachment) => (
          <Group key={attachment.id} justify="space-between">
            <Anchor onClick={() => void handleDownload(attachment)} style={{ cursor: "pointer" }}>
              {attachment.filename}
            </Anchor>
            <Text size="xs" c="dimmed">
              {formatSize(attachment.size)}
            </Text>
          </Group>
        ))}
        {attachments?.length === 0 ? (
          <Text size="sm" c="dimmed">
            No attachments yet.
          </Text>
        ) : null}
      </Stack>

      <input
        ref={fileInputRef}
        type="file"
        disabled={uploading}
        onChange={(event) => {
          const file = event.currentTarget.files?.[0];
          if (file) void handleFileSelected(file);
        }}
      />
      {uploadError ? (
        <Text size="sm" c="red" mt="xs">
          {uploadError}
        </Text>
      ) : null}
    </Card>
  );
}

/**
 * `RecordDetail` already covers every generic field + `WorkflowActionBar` + edit/delete for
 * `jira.issues` for free — the only things genuinely missing for this entity specifically are a
 * comment thread, attachments, and a few real pickers/panels, so this page composes the generic
 * component with those rather than re-implementing field rendering by hand. `assigneeEmail`/
 * `originalEstimateMinutes` are fetched separately here (not exposed by `RecordDetail`, a shared
 * component with no render-prop for its internal fetch) — `AssigneePicker`/`WorklogsPanel` need
 * them directly.
 */
export function IssueDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { data: issue } = useApiQuery<{ data: IssueRecord }, IssueRecord>(
    ["record", "jira.issues", id],
    `/api/jira.issues/${id}`,
    (response) => response.data,
    Boolean(id),
  );

  if (!id) return <div>Missing issue id</div>;

  return (
    <>
      <RecordDetail entityName="jira.issues" id={id} />
      <Container>
        <AssigneePicker issueId={id} currentEmail={issue?.data.assigneeEmail ?? null} />
        <IssueLinksPanel issueId={id} />
        <SubtasksPanel issueId={id} />
        <WorklogsPanel
          issueId={id}
          originalEstimateMinutes={issue?.data.originalEstimateMinutes ?? null}
        />
        <WatchersPanel issueId={id} />
        <CommentsPanel issueId={id} />
        <AttachmentsPanel issueId={id} />
      </Container>
    </>
  );
}
