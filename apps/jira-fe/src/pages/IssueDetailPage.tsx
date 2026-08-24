import { useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Anchor, Button, Card, Container, Group, Stack, Text, Textarea, TextInput, Title } from "@mantine/core";
import { ApiErrorMessage, RecordDetail, useApiMutation, useApiQuery, useAuth } from "@metap/platform-react";
import type { AttachmentRecord, CommentRecord, ListResponse } from "../api/types";

/**
 * `jira.comments` has a `filters: ["issue"]` list_view (`comment_entity.rs`) — filtering by
 * `issue` already works through the generic `QueryPlanner`, so this panel needed no backend
 * change, only the UI `RecordDetail`'s generic field renderer doesn't have (no "related records"
 * concept there).
 */
function CommentsPanel({ issueId }: { issueId: string }) {
  const queryClient = useQueryClient();
  const [authorEmail, setAuthorEmail] = useState("");
  const [body, setBody] = useState("");

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

  const addComment = useApiMutation<{ data: CommentRecord }, { data: { issue: string; authorEmail: string; body: string } }>(
    "POST",
    "/api/jira.comments",
  );

  async function handleSubmit() {
    await addComment.mutateAsync({ data: { issue: issueId, authorEmail, body } });
    setBody("");
    void queryClient.invalidateQueries({ queryKey: commentsQueryKey });
  }

  return (
    <Card withBorder mt="md" padding="md">
      <Title order={4} mb="sm">
        Comments
      </Title>

      {isLoading ? <Text size="sm">Loading…</Text> : null}
      {error ? <ApiErrorMessage error={error} /> : null}

      <Stack gap="sm" mb="md">
        {(comments ?? []).map((comment) => (
          <Card key={comment.id} withBorder padding="sm" bg="var(--mantine-color-gray-0)">
            <Text size="sm" fw={600}>
              {comment.data.authorEmail}
            </Text>
            <Text size="sm">{comment.data.body}</Text>
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
    const response = await fetch(`/api/jira.issues/${issueId}/attachments/${attachment.id}/download`, {
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    });
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
 * comment thread and attachments, so this page composes the generic component with 2 panels
 * rather than re-implementing field rendering by hand.
 */
export function IssueDetailPage() {
  const { id } = useParams<{ id: string }>();
  if (!id) return <div>Missing issue id</div>;

  return (
    <>
      <RecordDetail entityName="jira.issues" id={id} />
      <Container>
        <CommentsPanel issueId={id} />
        <AttachmentsPanel issueId={id} />
      </Container>
    </>
  );
}
