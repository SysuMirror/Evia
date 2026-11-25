import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type ArchiveManifest,
  type BackfillJob,
  type Environment,
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
  PageHeading,
  Select,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import {
  BACKFILL_NAME_MAX,
  backfillStatusLabel,
  backfillStatusTone,
  defaultBackfillForm,
  environmentLabels,
  environmentOptions,
  formatByteSize,
  formatInstant,
  isActiveBackfill,
  isBackfillable,
  manifestStatusLabel,
  manifestStatusTone,
  shortHash,
  validateBackfillForm,
  type ArchiveEnvironment,
  type BackfillFormState,
} from './model';
import { useArchivingRequest } from './use-archiving-request';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

const PAGE_LIMIT = 20;
const ACTIVE_POLL_MS = 2000;

export function ArchivingPage({ project, session, environment }: PageProps) {
  const cache = useQueryClient();
  const toast = useToast();
  // Archive and backfill are owner-only lifecycle governance.
  const allowed = project.permissions.includes('settings.write');
  const requests = useArchivingRequest(
    project,
    session,
    `${project.id}:${session.user_id}:${session.csrf_token}`,
    allowed,
  );

  const [selectedEnv, setSelectedEnv] = useState<ArchiveEnvironment>(
    environment as ArchiveEnvironment,
  );
  const [form, setForm] = useState<BackfillFormState>(defaultBackfillForm());
  const [formErrors, setFormErrors] = useState<string[]>([]);
  const [notice, setNotice] = useState('');

  const manifestKey = ['archive-manifests', project.id, selectedEnv] as const;
  const manifests = useQuery({
    queryKey: manifestKey,
    queryFn: ({ signal }) =>
      api.listArchiveManifests(project.id, { environment: selectedEnv, limit: PAGE_LIMIT }, signal),
    enabled: allowed,
  });

  const backfillKey = ['backfill-jobs', project.id, selectedEnv] as const;
  const backfills = useQuery({
    queryKey: backfillKey,
    queryFn: ({ signal }) => api.listBackfillJobs(project.id, { limit: PAGE_LIMIT }, signal),
    enabled: allowed,
    refetchInterval: (query) => {
      const data = query.state.data;
      return data?.items.some(isActiveBackfill) ? ACTIVE_POLL_MS : false;
    },
  });

  const materialized = useMemo(
    () => (manifests.data?.items ?? []).filter(isBackfillable),
    [manifests.data],
  );

  const manifestOptions = useMemo(
    () => [
      { value: '', label: materialized.length ? '请选择归档清单' : '当前环境暂无可回补清单' },
      ...materialized.map((manifest) => ({
        value: manifest.manifest_id,
        label: `${shortHash(manifest.manifest_id)} | ${manifest.row_count} 行 | ${formatInstant(manifest.created_at)}`,
      })),
    ],
    [materialized],
  );

  const refresh = () => {
    void cache.invalidateQueries({ queryKey: ['archive-manifests', project.id] });
    void cache.invalidateQueries({ queryKey: ['backfill-jobs', project.id] });
  };

  const patch = (partial: Partial<BackfillFormState>) => {
    setForm((current) => ({ ...current, ...partial }));
    setFormErrors([]);
  };

  const submit = () => {
    const errors = validateBackfillForm(form);
    if (errors.length > 0) {
      setFormErrors(errors);
      return;
    }
    const key = crypto.randomUUID();
    void requests.perform(
      'backfill.create',
      (signal) =>
        api.createBackfillJob(
          project.id,
          {
            name: form.name.trim(),
            environment: selectedEnv,
            manifest_id: form.manifestId,
          },
          key,
          session.csrf_token,
          signal,
        ),
      (job: BackfillJob) => {
        setForm(defaultBackfillForm());
        setFormErrors([]);
        refresh();
        toast.success(`回补作业 ${job.job_id.slice(0, 8)}… 已入队。`);
      },
      (error) => {
        setNotice(
          error instanceof ApiError && error.status === 409
            ? '幂等键冲突或请求重复，请核对后重试。'
            : error.message,
        );
      },
    );
  };

  const cancelJob = (job: BackfillJob) => {
    void requests.perform(
      'backfill.cancel',
      (signal) => api.cancelBackfillJob(project.id, job.job_id, session.csrf_token, signal),
      () => {
        refresh();
        toast.success('排队中的回补作业已取消。');
      },
      (error) => setNotice(error.message),
    );
  };

  const manifestColumns: DataTableColumn<ArchiveManifest>[] = [
    {
      key: 'created_at',
      header: '创建时间',
      render: (row) => formatInstant(row.created_at),
    },
    {
      key: 'status',
      header: '状态',
      render: (row) => (
        <Badge tone={manifestStatusTone(row.status)}>{manifestStatusLabel(row.status)}</Badge>
      ),
    },
    {
      key: 'cutoff_at',
      header: '清理截止点',
      render: (row) => formatInstant(row.cutoff_at),
    },
    {
      key: 'window',
      header: '事件覆盖窗口',
      render: (row) =>
        `${formatInstant(row.window_start_at)} ~ ${formatInstant(row.window_end_at)}`,
    },
    {
      key: 'row_count',
      header: '行数',
      render: (row) => String(row.row_count),
    },
    {
      key: 'artifact_bytes',
      header: '产物大小',
      render: (row) => formatByteSize(row.artifact_bytes),
    },
    {
      key: 'artifact_sha256',
      header: '校验和',
      render: (row) => shortHash(row.artifact_sha256),
    },
    {
      key: 'error_code',
      header: '失败原因',
      render: (row) => row.error_code ?? '—',
    },
  ];

  const backfillColumns: DataTableColumn<BackfillJob>[] = [
    {
      key: 'created_at',
      header: '创建时间',
      render: (row) => formatInstant(row.created_at),
    },
    {
      key: 'name',
      header: '名称',
      render: (row) => row.name,
    },
    {
      key: 'state',
      header: '状态',
      render: (row) => (
        <Badge tone={backfillStatusTone(row.state)}>{backfillStatusLabel(row.state)}</Badge>
      ),
    },
    {
      key: 'manifest_id',
      header: '归档清单',
      render: (row) => shortHash(row.manifest_id),
    },
    {
      key: 'rows',
      header: '行数（已插入/预期）',
      render: (row) => `${row.rows_inserted} / ${row.rows_expected}`,
    },
    {
      key: 'error_code',
      header: '失败原因',
      render: (row) => row.error_code ?? '—',
    },
    {
      key: 'actions',
      header: '操作',
      kind: 'actions',
      render: (row) =>
        row.state === 'queued' ? (
          <Button variant="secondary" disabled={!!requests.busy} onClick={() => cancelJob(row)}>
            取消
          </Button>
        ) : (
          '—'
        ),
    },
  ];

  const saving = requests.busy === 'backfill.create';

  return (
    <>
      <PageHeading title="归档与回补" />
      <div className="archiving-intro inline-notice">
        <Alert
          tone="info"
          title="强制执行保留清理前，过期事实会先物化为带校验和的 JSONL 归档清单；回补作业把归档产物重新写入 ClickHouse，事件视图按身份去重，重复回补不会改变查询结果。"
        />
      </div>

      <DataSurface label="发起数据回补">
        {notice ? (
          <Alert
            tone="error"
            title={notice}
            action={<Button onClick={() => setNotice('')}>关闭</Button>}
          />
        ) : null}
        <form
          className="console-form archiving-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!saving) submit();
          }}
        >
          <div className="archiving-fields">
            <Field label="环境" htmlFor="archiving-environment">
              <Select
                id="archiving-environment"
                value={selectedEnv}
                options={environmentOptions}
                disabled={!!requests.busy}
                onChange={(event) => setSelectedEnv(event.target.value as ArchiveEnvironment)}
              />
            </Field>
            <Field
              label="回补名称"
              htmlFor="backfill-name"
              hint={`1 到 ${BACKFILL_NAME_MAX} 字符。`}
            >
              <Input
                id="backfill-name"
                value={form.name}
                maxLength={BACKFILL_NAME_MAX}
                disabled={!!requests.busy}
                onChange={(event) => patch({ name: event.target.value })}
              />
            </Field>
            <Field
              label="归档清单"
              htmlFor="backfill-manifest"
              error={formErrors.find((value) => value.includes('归档清单'))}
            >
              <Select
                id="backfill-manifest"
                value={form.manifestId}
                options={manifestOptions}
                disabled={!!requests.busy || materialized.length === 0}
                onChange={(event) => patch({ manifestId: event.target.value })}
              />
            </Field>
          </div>
          {formErrors.length > 0 ? <Alert tone="error" title={formErrors.join(' ')} /> : null}
          <div className="archiving-actions">
            <Button
              type="submit"
              loading={saving}
              disabled={!!requests.busy || materialized.length === 0}
            >
              发起回补
            </Button>
            <span className="evia-field-hint">
              当前环境：{environmentLabels[selectedEnv]}；仅已物化清单可回补。
            </span>
          </div>
        </form>
      </DataSurface>

      <DataSurface label="回补作业">
        {backfills.isError ? (
          <Alert
            tone="error"
            title="无法读取回补作业"
            action={<Button onClick={() => void backfills.refetch()}>重试</Button>}
          >
            {backfills.error instanceof ApiError ? backfills.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <DataTable
            caption="数据回补作业列表"
            columns={backfillColumns}
            rows={backfills.data?.items ?? []}
            rowKey={(row) => row.job_id}
            minWidth={760}
            loading={backfills.isFetching}
            empty={<EmptyPanel description="还没有回补作业。" />}
          />
        )}
      </DataSurface>

      <DataSurface label="归档清单">
        {manifests.isError ? (
          <Alert
            tone="error"
            title="无法读取归档清单"
            action={<Button onClick={() => void manifests.refetch()}>重试</Button>}
          >
            {manifests.error instanceof ApiError ? manifests.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <DataTable
            caption="归档清单列表"
            columns={manifestColumns}
            rows={manifests.data?.items ?? []}
            rowKey={(row) => row.manifest_id}
            minWidth={900}
            loading={manifests.isFetching}
            empty={<EmptyPanel description="当前环境还没有归档清单。" />}
          />
        )}
      </DataSurface>
    </>
  );
}
