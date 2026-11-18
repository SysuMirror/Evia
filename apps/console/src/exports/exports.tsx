import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type ExportJob,
  type Project,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  Modal,
  PageHeading,
  PaginationBar,
  Select,
  Toolbar,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import {
  defaultExportForm,
  EXPORT_LIMIT_MAX,
  EXPORT_LIMIT_MIN,
  EXPORT_NAME_MAX,
  exportSourceLabel,
  exportStateLabel,
  exportStateTone,
  formatByteSize,
  formatInstant,
  hasActiveExportJob,
  isExportTerminal,
  maxWindowDays,
  parseEventKeys,
  toExportInput,
  type ExportFormState,
  type ExportFormat,
  type ExportKind,
} from './model';
import { useExportRequest } from './use-export-request';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

const PAGE_LIMIT = 20;
const ACTIVE_POLL_MS = 2000;

const environmentOptions = [
  { value: 'development', label: '开发环境' },
  { value: 'staging', label: '预发环境' },
  { value: 'production', label: '生产环境' },
];

const kindOptions: { value: ExportKind; label: string }[] = [
  { value: 'trend', label: '趋势查询（最长 31 天）' },
  { value: 'events', label: '事件明细（最长 7 天）' },
];

const formatOptions: { value: ExportFormat; label: string }[] = [
  { value: 'json', label: 'JSON 完整结果文档' },
  { value: 'csv', label: 'CSV 表格（按结果列顺序）' },
  { value: 'ndjson', label: 'NDJSON 每行一条结果记录' },
];

function shortJobID(jobID: string): string {
  return jobID.length > 13 ? `${jobID.slice(0, 8)}…${jobID.slice(-4)}` : jobID;
}

export function ExportsPage({ project, session, environment }: PageProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const allowed = project.permissions.includes('data.export');
  const requests = useExportRequest(
    project,
    session,
    `${project.id}:${session.user_id}:${session.csrf_token}`,
    allowed,
  );
  const [form, setForm] = useState<ExportFormState>({
    ...defaultExportForm(environment),
  });
  const [formErrors, setFormErrors] = useState<string[]>([]);
  const [notice, setNotice] = useState('');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [cursors, setCursors] = useState<string[]>([]);

  const queryKey = ['exports', project.id, session.user_id, cursors.at(-1) ?? ''] as const;
  const jobs = useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      api.listExportJobs(project.id, { limit: PAGE_LIMIT, cursor: cursors.at(-1) }, signal),
    enabled: allowed,
    refetchInterval: (query) => {
      const data = query.state.data;
      return data && hasActiveExportJob(data.items) ? ACTIVE_POLL_MS : false;
    },
  });

  const refresh = () => cache.invalidateQueries({ queryKey: ['exports', project.id] });
  const patch = (partial: Partial<ExportFormState>) => {
    setForm((current) => ({ ...current, ...partial }));
    setFormErrors([]);
    setNotice('');
  };

  if (!allowed) {
    return (
      <>
        <PageHeading title="数据导出" />
        <Alert
          tone="error"
          title="你没有数据导出权限，仅项目所有者或持有 data.export 授权的主体可以管理导出作业。"
        />
      </>
    );
  }
  if (requests.denied) {
    return (
      <>
        <PageHeading title="数据导出" />
        <Alert tone="error" title={requests.denied.message} />
      </>
    );
  }

  const openConfirm = () => {
    const { errors } = toExportInput(form);
    if (errors.length > 0) {
      setFormErrors(errors);
      return;
    }
    setFormErrors([]);
    setConfirmOpen(true);
  };

  const submit = () => {
    const built = toExportInput(form);
    if (!built.input) {
      setFormErrors(built.errors);
      setConfirmOpen(false);
      return;
    }
    const input = built.input;
    const key = crypto.randomUUID();
    void requests.perform(
      'export.create',
      (signal) => api.createExportJob(project.id, input, key, session.csrf_token, signal),
      (job: ExportJob) => {
        setConfirmOpen(false);
        setForm({ ...defaultExportForm(form.environment) });
        refresh();
        toast.success(`导出作业 ${shortJobID(job.job_id)} 已入队，完成后可申请一次性下载授权。`);
      },
      (error) => {
        setConfirmOpen(false);
        setNotice(
          error instanceof ApiError && error.status === 409
            ? '幂等键冲突或请求重复，请核对后重试。'
            : error.message,
        );
      },
    );
  };

  const cancelJob = (job: ExportJob) => {
    void requests.perform(
      'export.cancel',
      (signal) => api.cancelExportJob(project.id, job.job_id, session.csrf_token, signal),
      () => {
        refresh();
        toast.success(`导出作业 ${shortJobID(job.job_id)} 已取消。`);
      },
      (error) => setNotice(error.message),
    );
  };

  const requestDownload = (job: ExportJob) => {
    void requests.perform(
      'export.grant',
      (signal) => api.createExportDownloadGrant(project.id, job.job_id, session.csrf_token, signal),
      (grant) => {
        // The capability token authenticates the download without a session;
        // a same-origin anchor keeps the one-time token out of request headers.
        const anchor = document.createElement('a');
        anchor.href = api.exportDownloadPath(grant.token);
        anchor.rel = 'noopener';
        anchor.click();
        toast.success(`下载授权已生成，将在 ${formatInstant(grant.expires_at)} 过期。`);
      },
      (error) => setNotice(error.message),
    );
  };

  const columns: DataTableColumn<ExportJob>[] = [
    {
      key: 'job_id',
      header: '作业',
      render: (row) => <code title={row.job_id}>{shortJobID(row.job_id)}</code>,
    },
    {
      key: 'name',
      header: '名称',
      render: (row) => row.name,
    },
    {
      key: 'source',
      header: '来源',
      render: (row) => exportSourceLabel(row),
    },
    {
      key: 'format',
      header: '格式',
      render: (row) => row.format.toUpperCase(),
    },
    {
      key: 'state',
      header: '状态',
      render: (row) => (
        <Badge tone={exportStateTone(row.state)}>{exportStateLabel(row.state)}</Badge>
      ),
    },
    {
      key: 'result_rows',
      header: '行数',
      render: (row) => (row.result_rows === null ? '—' : String(row.result_rows)),
    },
    {
      key: 'artifact_bytes',
      header: '产物大小',
      render: (row) => formatByteSize(row.artifact_bytes),
    },
    {
      key: 'created_at',
      header: '创建时间',
      render: (row) => formatInstant(row.created_at),
    },
    {
      key: 'actions',
      header: '操作',
      render: (row) => {
        if (row.state === 'queued') {
          return (
            <Button variant="secondary" disabled={!!requests.busy} onClick={() => cancelJob(row)}>
              取消
            </Button>
          );
        }
        if (row.state === 'completed') {
          return (
            <Button
              variant="secondary"
              loading={requests.busy === 'export.grant'}
              onClick={() => requestDownload(row)}
            >
              申请下载
            </Button>
          );
        }
        return <span className="evia-field-hint">—</span>;
      },
    },
  ];

  const activeCount = useMemo(
    () => (jobs.data?.items ?? []).filter((job) => !isExportTerminal(job.state)).length,
    [jobs.data],
  );
  const ceiling = maxWindowDays(form.kind);

  return (
    <>
      <PageHeading title="数据导出" />
      <div className="export-intro inline-notice">
        <Alert
          tone="info"
          title="导出复用有界分析查询，结果物化为 JSON 文件；下载采用一次性授权令牌（15 分钟内有效、兑换即失效），全程审计留痕。"
        />
      </div>
      <DataSurface label="发起数据导出">
        <Toolbar label="导出请求">
          <div className="export-fields">
            <Field label="导出名称" htmlFor="export-name">
              <Input
                id="export-name"
                value={form.name}
                maxLength={EXPORT_NAME_MAX}
                placeholder="例如：11 月第一周行为趋势"
                autoComplete="off"
                disabled={!!requests.busy}
                onChange={(event) => patch({ name: event.target.value })}
              />
            </Field>
            <Field label="环境" htmlFor="export-environment">
              <Select
                id="export-environment"
                value={form.environment}
                options={environmentOptions}
                disabled={!!requests.busy}
                onChange={(event) =>
                  patch({ environment: event.target.value as ExportFormState['environment'] })
                }
              />
            </Field>
            <Field label="产物格式" htmlFor="export-format">
              <Select
                id="export-format"
                value={form.format}
                options={formatOptions}
                disabled={!!requests.busy}
                onChange={(event) => patch({ format: event.target.value as ExportFormat })}
              />
            </Field>
            <Field label="查询类型" htmlFor="export-kind">
              <Select
                id="export-kind"
                value={form.kind}
                options={kindOptions}
                disabled={!!requests.busy}
                onChange={(event) => {
                  const kind = event.target.value as ExportKind;
                  const nextCeiling = maxWindowDays(kind);
                  patch({
                    kind,
                    windowDays: Math.min(form.windowDays, nextCeiling),
                    eventKeys: kind === 'events' ? form.eventKeys : '',
                  });
                }}
              />
            </Field>
            <Field label={`时间窗口（1-${ceiling} 天）`} htmlFor="export-window">
              <Input
                id="export-window"
                type="number"
                min={1}
                max={ceiling}
                value={String(form.windowDays)}
                disabled={!!requests.busy}
                onChange={(event) => patch({ windowDays: Number(event.target.value) })}
              />
            </Field>
            <Field
              label={`结果行数上限（${EXPORT_LIMIT_MIN}-${EXPORT_LIMIT_MAX}）`}
              htmlFor="export-limit"
            >
              <Input
                id="export-limit"
                type="number"
                min={EXPORT_LIMIT_MIN}
                max={EXPORT_LIMIT_MAX}
                value={String(form.limit)}
                disabled={!!requests.busy}
                onChange={(event) => patch({ limit: Number(event.target.value) })}
              />
            </Field>
            {form.kind === 'events' && (
              <Field label="事件标识（可选，逗号分隔，最多 20 个）" htmlFor="export-event-keys">
                <Input
                  id="export-event-keys"
                  value={form.eventKeys}
                  placeholder="例如：lesson_opened,lesson_completed"
                  autoComplete="off"
                  disabled={!!requests.busy}
                  onChange={(event) => patch({ eventKeys: event.target.value })}
                />
              </Field>
            )}
          </div>
        </Toolbar>
        {formErrors.length > 0 && (
          <div className="inline-notice">
            <Alert tone="error" title={formErrors.join(' ')} />
          </div>
        )}
        {notice && (
          <div className="inline-notice">
            <Alert tone="error" title={notice} />
          </div>
        )}
        <div className="inline-actions export-submit">
          <Button
            variant="primary"
            loading={requests.busy === 'export.create'}
            onClick={openConfirm}
          >
            发起导出
          </Button>
          <span className="evia-field-hint">
            {form.kind === 'events' ? parseEventKeys(form.eventKeys).length : 0}{' '}
            个事件标识，窗口上限 {ceiling} 天。
          </span>
        </div>
      </DataSurface>

      <DataSurface label="导出作业">
        {jobs.isError ? (
          <Alert
            tone="error"
            title="无法读取导出作业"
            action={<Button onClick={() => void jobs.refetch()}>重试</Button>}
          >
            {jobs.error instanceof ApiError ? jobs.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <>
            {activeCount > 0 && (
              <p className="evia-field-hint export-polling">
                {activeCount} 个作业进行中，列表每 {ACTIVE_POLL_MS / 1000} 秒自动刷新。
              </p>
            )}
            <DataTable
              caption="数据导出作业列表"
              columns={columns}
              rows={jobs.data?.items ?? []}
              rowKey={(row) => row.job_id}
              minWidth={720}
              loading={jobs.isFetching}
              empty={<EmptyPanel description="还没有导出作业。" />}
            />
            <PaginationBar
              page={cursors.length + 1}
              count={jobs.data?.items.length ?? 0}
              hasNext={!!jobs.data?.next_cursor}
              busy={jobs.isFetching || !!requests.busy}
              onPrevious={() => setCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (jobs.data?.next_cursor)
                  setCursors((current) => [...current, jobs.data!.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>

      <Modal
        open={confirmOpen}
        title="确认发起数据导出"
        busy={requests.busy === 'export.create'}
        onRequestClose={() => {
          if (!requests.busy) setConfirmOpen(false);
        }}
        footer={
          <>
            <Button disabled={!!requests.busy} onClick={() => setConfirmOpen(false)}>
              取消
            </Button>
            <Button variant="primary" loading={requests.busy === 'export.create'} onClick={submit}>
              确认导出
            </Button>
          </>
        }
      >
        <p>
          将在{' '}
          <strong>
            {environmentOptions.find((item) => item.value === form.environment)?.label}
          </strong>{' '}
          导出最近 {form.windowDays} 天的{form.kind === 'trend' ? '趋势' : '事件明细'}数据，行数上限{' '}
          {form.limit}，产物格式为 {form.format.toUpperCase()}。
        </p>
        <p className="evia-field-hint">名称：{form.name.trim()}</p>
      </Modal>
    </>
  );
}
