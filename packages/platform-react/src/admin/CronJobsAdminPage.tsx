import { Fragment, useState } from "react";
import {
  Alert,
  Button,
  Container,
  Group,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  Textarea,
  TextInput,
  Title,
} from "@mantine/core";
import { useTranslation } from "react-i18next";
import { ApiError } from "../api/client";
import { ApiErrorMessage } from "../api/ApiErrorMessage";
import {
  useAdminCronJobActions,
  useAdminCronJobs,
  useCreateAdminCronJob,
  useCronJobRuns,
} from "./adminApi";

const TARGET_TYPES = ["workflow_transition", "bulk_query_action", "webhook"];
const DISPATCH_MODES = ["outbox", "direct"];

function CronJobRuns({ jobId }: { jobId: string }) {
  const { t } = useTranslation();
  const { data: runs, isLoading, error } = useCronJobRuns(jobId);

  if (isLoading) {
    return <Text size="sm">{t("common.loading")}</Text>;
  }
  if (error) {
    return <ApiErrorMessage error={error} />;
  }

  return (
    <Table>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>{t("admin.cronJobs.runs.status")}</Table.Th>
          <Table.Th>{t("admin.cronJobs.runs.scheduledFor")}</Table.Th>
          <Table.Th>{t("admin.cronJobs.runs.finishedAt")}</Table.Th>
          <Table.Th>{t("admin.cronJobs.runs.error")}</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {(runs ?? []).length === 0 ? (
          <Table.Tr>
            <Table.Td colSpan={4}>{t("common.noRecords")}</Table.Td>
          </Table.Tr>
        ) : (
          (runs ?? []).map((run) => (
            <Table.Tr key={run.id}>
              <Table.Td>{run.status}</Table.Td>
              <Table.Td>{run.scheduledFor}</Table.Td>
              <Table.Td>{run.finishedAt ?? "—"}</Table.Td>
              <Table.Td>{run.error ?? "—"}</Table.Td>
            </Table.Tr>
          ))
        )}
      </Table.Tbody>
    </Table>
  );
}

export function CronJobsAdminPage() {
  const { t } = useTranslation();
  const { data: jobs, isLoading, error, refetch } = useAdminCronJobs();
  const createJob = useCreateAdminCronJob();
  const { toggleEnabled, deleteJob } = useAdminCronJobActions();

  const [name, setName] = useState("");
  const [cronExpr, setCronExpr] = useState("");
  const [timezone, setTimezone] = useState("UTC");
  const [targetType, setTargetType] = useState<string>(TARGET_TYPES[0] ?? "webhook");
  const [dispatchMode, setDispatchMode] = useState<string>(DISPATCH_MODES[0] ?? "outbox");
  const [targetConfigText, setTargetConfigText] = useState("{}");
  const [targetConfigError, setTargetConfigError] = useState<string | null>(null);
  const [rowError, setRowError] = useState<string | null>(null);
  const [expandedJobId, setExpandedJobId] = useState<string | null>(null);

  async function handleCreate() {
    setTargetConfigError(null);
    let targetConfig: unknown;
    try {
      targetConfig = JSON.parse(targetConfigText || "{}");
    } catch {
      setTargetConfigError(t("common.invalidJson"));
      return;
    }

    try {
      await createJob.mutateAsync({
        name,
        cronExpr,
        timezone,
        targetType,
        targetConfig,
        dispatchMode,
        enabled: true,
      });
      setName("");
      setCronExpr("");
      setTargetConfigText("{}");
      await refetch();
    } catch {
      // surfaced via createJob.error below
    }
  }

  async function handleDelete(id: string) {
    if (!window.confirm(t("common.deleteConfirm"))) {
      return;
    }
    setRowError(null);
    try {
      await deleteJob(id);
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : t("common.somethingWentWrong"));
    }
  }

  return (
    <Container py="xl">
      <Title order={2} mb="md">
        {t("admin.cronJobs.title")}
      </Title>

      <Stack mb="xl" maw={480}>
        <Title order={4}>{t("admin.cronJobs.createTitle")}</Title>
        {createJob.error ? (
          <Alert color="red">
            {createJob.error instanceof ApiError ? createJob.error.message : t("common.somethingWentWrong")}
          </Alert>
        ) : null}
        <TextInput
          label={t("admin.cronJobs.name")}
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <TextInput
          label={t("admin.cronJobs.cronExpr")}
          description={t("admin.cronJobs.cronExprDescription")}
          value={cronExpr}
          onChange={(event) => setCronExpr(event.currentTarget.value)}
        />
        <TextInput
          label={t("admin.cronJobs.timezone")}
          value={timezone}
          onChange={(event) => setTimezone(event.currentTarget.value)}
        />
        <Select
          label={t("admin.cronJobs.targetType")}
          data={TARGET_TYPES}
          value={targetType}
          onChange={(value) => setTargetType(value ?? TARGET_TYPES[0]!)}
          allowDeselect={false}
        />
        <Select
          label={t("admin.cronJobs.dispatchMode")}
          data={DISPATCH_MODES}
          value={dispatchMode}
          onChange={(value) => setDispatchMode(value ?? DISPATCH_MODES[0]!)}
          allowDeselect={false}
        />
        <Textarea
          label={t("admin.cronJobs.targetConfig")}
          description={t("admin.cronJobs.targetConfigDescription")}
          value={targetConfigText}
          onChange={(event) => setTargetConfigText(event.currentTarget.value)}
          error={targetConfigError}
          autosize
          minRows={3}
        />
        <Button
          onClick={() => void handleCreate()}
          loading={createJob.isPending}
          disabled={name.trim().length === 0 || cronExpr.trim().length === 0}
        >
          {t("common.new")}
        </Button>
      </Stack>

      {rowError ? (
        <Alert color="red" mb="md" onClose={() => setRowError(null)} withCloseButton>
          {rowError}
        </Alert>
      ) : null}

      {isLoading ? (
        <Text>{t("common.loading")}</Text>
      ) : error ? (
        <ApiErrorMessage error={error} />
      ) : (
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t("admin.cronJobs.name")}</Table.Th>
              <Table.Th>{t("admin.cronJobs.cronExpr")}</Table.Th>
              <Table.Th>{t("admin.cronJobs.timezone")}</Table.Th>
              <Table.Th>{t("admin.cronJobs.targetType")}</Table.Th>
              <Table.Th>{t("admin.cronJobs.dispatchMode")}</Table.Th>
              <Table.Th>{t("admin.cronJobs.nextRunAt")}</Table.Th>
              <Table.Th>{t("admin.cronJobs.enabled")}</Table.Th>
              <Table.Th>{t("common.actions")}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(jobs ?? []).map((job) => (
              <Fragment key={job.id}>
                <Table.Tr>
                  <Table.Td>{job.name}</Table.Td>
                  <Table.Td>{job.cronExpr}</Table.Td>
                  <Table.Td>{job.timezone}</Table.Td>
                  <Table.Td>{job.targetType}</Table.Td>
                  <Table.Td>{job.dispatchMode}</Table.Td>
                  <Table.Td>{job.nextRunAt}</Table.Td>
                  <Table.Td>
                    <Switch checked={job.enabled} onChange={() => void toggleEnabled(job)} />
                  </Table.Td>
                  <Table.Td>
                    <Group gap="xs" wrap="nowrap">
                      <Button
                        variant="subtle"
                        size="compact-sm"
                        onClick={() => setExpandedJobId((current) => (current === job.id ? null : job.id))}
                      >
                        {expandedJobId === job.id ? t("workflow.hide") : t("admin.cronJobs.runs.title")}
                      </Button>
                      <Button
                        color="red"
                        variant="subtle"
                        size="compact-sm"
                        onClick={() => void handleDelete(job.id)}
                      >
                        {t("common.delete")}
                      </Button>
                    </Group>
                  </Table.Td>
                </Table.Tr>
                {expandedJobId === job.id ? (
                  <Table.Tr>
                    <Table.Td colSpan={8}>
                      <CronJobRuns jobId={job.id} />
                    </Table.Td>
                  </Table.Tr>
                ) : null}
              </Fragment>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Container>
  );
}
