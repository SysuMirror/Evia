import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type ErasureJob,
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
  Textarea,
  Toolbar,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import {
  defaultErasureForm,
  ERASURE_REASON_MAX,
  erasureStateLabel,
  erasureStateTone,
  formatBackupExpiry,
  hasActiveErasureJob,
  isErasureTerminal,
  toErasureInput,
  type ErasureFormState,
  type SubjectKind,
} from './model';
import { useErasureRequest } from './use-erasure-request';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

const PAGE_LIMIT = 20;
const ACTIVE_POLL_MS = 2000;

const environmentOptions = [
  { value: 'development', label: '开发环境' },
  { value: 'staging', label: '预发环境' },
  { value: 'production', label: '生产环境' },
];

const subjectOptions: { value: SubjectKind; label: string }[] = [
  { value: 'uid', label: '用户标识 uid' },
  { value: 'anonymous_id', label: '匿名标识 anonymous_id（UUID）' },
];

function shortJobID(jobID: string): string {
  return jobID.length > 13 ? `${jobID.slice(0, 8)}…${jobID.slice(-4)}` : jobID;
}

export function ErasurePage({ project, session, environment }: PageProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const allowed = project.permissions.includes('data.erasure');
  const requests = useErasureRequest(
    project,
    session,
    `${project.id}:${session.user_id}:${session.csrf_token}`,
    allowed,
  );
  const [form, setForm] = useState<ErasureFormState>({
    ...defaultErasureForm(),
    environment,
  });
  const [formErrors, setFormErrors] = useState<string[]>([]);
  const [notice, setNotice] = useState('');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [cursors, setCursors] = useState<string[]>([]);

  const queryKey = ['erasure', project.id, session.user_id, cursors.at(-1) ?? ''] as const;
  const jobs = useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      api.listErasureJobs(project.id, { limit: PAGE_LIMIT, cursor: cursors.at(-1) }, signal),
    enabled: allowed,
    refetchInterval: (query) => {
      const data = query.state.data;
      return data && hasActiveErasureJob(data.items) ? ACTIVE_POLL_MS : false;
    },
  });

  const refresh = () => cache.invalidateQueries({ queryKey: ['erasure', project.id] });
  const patch = (partial: Partial<ErasureFormState>) => {
    setForm((current) => ({ ...current, ...partial }));
    setFormErrors([]);
    setNotice('');
  };

  if (!allowed) {
    return (
      <>
        <PageHeading title="数据删除" />
        <Alert tone="error" title="你没有数据删除权限，仅项目所有者可以管理删除作业。" />
      </>
    );
  }
  if (requests.denied) {
    return (
      <>
        <PageHeading title="数据删除" />
        <Alert tone="error" title={requests.denied.message} />
      </>
    );
  }

  const openConfirm = () => {
    const { errors } = toErasureInput(form);
    if (errors.length > 0) {
      setFormErrors(errors);
      return;
    }
    setFormErrors([]);
    setConfirmOpen(true);
  };

  const submit = () => {
    const built = toErasureInput(form);
    if (!built.input) {
      setFormErrors(built.errors);
      setConfirmOpen(false);
      return;
    }
    const input = built.input;
    const key = crypto.randomUUID();
    void requests.perform(
      'erasure.create',
      (signal) => api.createErasureJob(project.id, input, key, session.csrf_token, signal),
      (job: ErasureJob) => {
        setConfirmOpen(false);
        setForm({ ...defaultErasureForm(), environment: form.environment });
        setNotice('');
        refresh();
        toast.success(`删除作业 ${shortJobID(job.job_id)} 已入队，删除屏障会立即生效。`);
      },
      (error) => {
        setConfirmOpen(false);
        setNotice(
          error instanceof ApiError && error.status === 409
            ? '该身份已有进行中的删除作业，或幂等键冲突，请核对后重试。'
            : error.message,
        );
      },
    );
  };

  const columns: DataTableColumn<ErasureJob>[] = [
    {
      key: 'job_id',
      header: '作业',
      render: (row) => <code title={row.job_id}>{shortJobID(row.job_id)}</code>,
    },
    {
      key: 'state',
      header: '状态',
      render: (row) => (
        <Badge tone={erasureStateTone(row.state)}>{erasureStateLabel(row.state)}</Badge>
      ),
    },
    {
      key: 'backup_expiry_at',
      header: '备份过期时间',
      render: (row) => formatBackupExpiry(row.backup_expiry_at),
    },
  ];

  const activeCount = useMemo(
    () => (jobs.data?.items ?? []).filter((job) => !isErasureTerminal(job.state)).length,
    [jobs.data],
  );

  return (
    <>
      <PageHeading title="数据删除" />
      <div className="erasure-intro inline-notice">
        <Alert
          tone="warning"
          title="数据删除不可逆：提交后会立即建立删除屏障，阻断该身份的新事实写入，并由 erasure-worker 异步清除历史事实。"
        />
      </div>
      <DataSurface label="发起数据删除">
        <Toolbar label="删除请求">
          <div className="erasure-fields">
            <Field label="环境" htmlFor="erasure-environment">
              <Select
                id="erasure-environment"
                value={form.environment}
                options={environmentOptions}
                disabled={!!requests.busy}
                onChange={(event) =>
                  patch({ environment: event.target.value as ErasureFormState['environment'] })
                }
              />
            </Field>
            <Field label="身份类型" htmlFor="erasure-subject-kind">
              <Select
                id="erasure-subject-kind"
                value={form.subjectKind}
                options={subjectOptions}
                disabled={!!requests.busy}
                onChange={(event) =>
                  patch({ subjectKind: event.target.value as SubjectKind, subjectValue: '' })
                }
              />
            </Field>
            <Field
              label={form.subjectKind === 'uid' ? '用户标识 uid' : '匿名标识 anonymous_id'}
              htmlFor="erasure-subject"
            >
              <Input
                id="erasure-subject"
                value={form.subjectValue}
                placeholder={form.subjectKind === 'uid' ? '要删除的 uid' : 'UUID 格式的匿名标识'}
                maxLength={128}
                autoComplete="off"
                disabled={!!requests.busy}
                onChange={(event) => patch({ subjectValue: event.target.value })}
              />
            </Field>
            <Field label="删除原因（审计必填）" htmlFor="erasure-reason">
              <Textarea
                id="erasure-reason"
                value={form.reason}
                maxLength={ERASURE_REASON_MAX}
                rows={3}
                placeholder="说明删除来源与依据，例如用户行使被遗忘权"
                disabled={!!requests.busy}
                onChange={(event) => patch({ reason: event.target.value })}
              />
            </Field>
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
        <div className="inline-actions erasure-submit">
          <Button
            variant="danger"
            loading={requests.busy === 'erasure.create'}
            onClick={openConfirm}
          >
            发起删除
          </Button>
          <span className="evia-field-hint">每个身份同时只允许一个进行中的删除作业。</span>
        </div>
      </DataSurface>

      <DataSurface label="删除作业">
        {jobs.isError ? (
          <Alert
            tone="error"
            title="无法读取删除作业"
            action={<Button onClick={() => void jobs.refetch()}>重试</Button>}
          >
            {jobs.error instanceof ApiError ? jobs.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <>
            {activeCount > 0 && (
              <p className="evia-field-hint erasure-polling">
                {activeCount} 个作业进行中，列表每 {ACTIVE_POLL_MS / 1000} 秒自动刷新。
              </p>
            )}
            <DataTable
              caption="数据删除作业列表"
              columns={columns}
              rows={jobs.data?.items ?? []}
              rowKey={(row) => row.job_id}
              minWidth={560}
              loading={jobs.isFetching}
              empty={<EmptyPanel description="还没有删除作业。" />}
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
        title="确认发起数据删除"
        busy={requests.busy === 'erasure.create'}
        onRequestClose={() => {
          if (!requests.busy) setConfirmOpen(false);
        }}
        footer={
          <>
            <Button disabled={!!requests.busy} onClick={() => setConfirmOpen(false)}>
              取消
            </Button>
            <Button variant="danger" loading={requests.busy === 'erasure.create'} onClick={submit}>
              确认删除
            </Button>
          </>
        }
      >
        <p>
          将在{' '}
          <strong>
            {environmentOptions.find((item) => item.value === form.environment)?.label}
          </strong>
          删除身份 <code>{form.subjectValue.trim()}</code>（
          {form.subjectKind === 'uid' ? 'uid' : 'anonymous_id'}
          ）的全部事实数据。删除屏障建立后，队列中该身份的重试消息也会被丢弃，无法恢复。
        </p>
        <p className="evia-field-hint">原因：{form.reason.trim()}</p>
      </Modal>
    </>
  );
}
