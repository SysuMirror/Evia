import { useEffect, useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type RetentionPolicy,
  type RetentionSweep,
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
  buildRetentionInput,
  environmentLabels,
  environmentOptions,
  formFromPolicy,
  formatPolicyInstant,
  formatSweepInstant,
  isRevisionConflict,
  isUncertainWrite,
  retentionSummary,
  RETENTION_MAX_DAYS,
  RETENTION_MIN_DAYS,
  sweepModeLabel,
  sweepStatusLabel,
  sweepStatusTone,
  type RetentionEnvironment,
  type RetentionFormState,
} from './model';
import { useRetentionRequest } from './use-retention-request';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

interface PendingIntent {
  revision: number;
  environment: RetentionEnvironment;
  retentionDays: number;
  key: string;
}

export function RetentionPage({ project, session, environment }: PageProps) {
  const cache = useQueryClient();
  const toast = useToast();
  // Retention is owner-only human governance; settings.write is owner-only.
  const allowed = project.permissions.includes('settings.write');
  const requests = useRetentionRequest(
    project,
    session,
    `${project.id}:${session.user_id}:${session.csrf_token}`,
    allowed,
  );

  const [selectedEnv, setSelectedEnv] = useState<RetentionEnvironment>(
    environment as RetentionEnvironment,
  );
  const [form, setForm] = useState<RetentionFormState>(formFromPolicy(undefined));
  const [formError, setFormError] = useState('');
  const [writeError, setWriteError] = useState('');
  const [pending, setPending] = useState<PendingIntent>();
  const [loadedRevision, setLoadedRevision] = useState<number | null>(null);

  const queryKey = ['retention', project.id, selectedEnv] as const;
  const policy = useQuery({
    queryKey,
    queryFn: ({ signal }) => api.getRetentionPolicy(project.id, selectedEnv, signal),
    enabled: allowed,
  });

  const sweeps = useQuery({
    queryKey: ['retention-sweeps', project.id, selectedEnv],
    queryFn: ({ signal }) =>
      api.listRetentionSweeps(project.id, { environment: selectedEnv, limit: 20 }, signal),
    enabled: allowed,
  });

  // Adopt server values whenever the policy for the selected environment
  // changes revision; never overwrite an in-progress edit or a retained intent.
  useEffect(() => {
    const current = policy.data;
    if (!current || current.revision === loadedRevision) return;
    setForm(formFromPolicy(current));
    setLoadedRevision(current.revision);
    setWriteError('');
  }, [policy.data, loadedRevision]);

  // Switching environments resets the editor to that scope's policy.
  useEffect(() => {
    setForm(formFromPolicy(policy.data));
    setFormError('');
    setWriteError('');
    setPending(undefined);
    setLoadedRevision(policy.data?.revision ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedEnv]);

  const current: RetentionPolicy | undefined = policy.data;
  const configured = (current?.retention_days ?? null) != null;
  const saving = requests.busy === 'retention.put';

  // Enable on any edit (including an invalid one) so submit can surface
  // validation errors; validity itself is enforced inside submit/buildRetentionInput.
  const currentDaysString = current?.retention_days != null ? String(current.retention_days) : '';
  const dirty = useMemo(
    () =>
      form.retentionDays.trim() !== currentDaysString.trim() ||
      form.environment !== (current?.environment ?? selectedEnv),
    [form, currentDaysString, current?.environment, selectedEnv],
  );

  if (!allowed) {
    return (
      <>
        <PageHeading title="数据保留" />
        <Alert tone="error" title="你没有保留策略管理权限，仅项目所有者可以配置数据保留。" />
      </>
    );
  }
  if (requests.denied) {
    return (
      <>
        <PageHeading title="数据保留" />
        <Alert tone="error" title={requests.denied.message} />
      </>
    );
  }

  const persist = (intent: PendingIntent) => {
    setWriteError('');
    void requests.perform(
      'retention.put',
      (signal) =>
        api.putRetentionPolicy(
          project.id,
          { environment: intent.environment, retention_days: intent.retentionDays },
          intent.revision,
          intent.key,
          session.csrf_token,
          signal,
        ),
      (updated: RetentionPolicy) => {
        setPending(undefined);
        cache.setQueryData(queryKey, updated);
        setForm(formFromPolicy(updated));
        setLoadedRevision(updated.revision);
        toast.success(
          `${environmentLabels[intent.environment]}保留策略已保存（修订版本 ${updated.revision}）。`,
        );
      },
      (error) => {
        if (isRevisionConflict(error)) {
          setPending(undefined);
          setWriteError('保留策略已被其他人修改，已为你重新加载，请核对后再提交。');
          void policy.refetch();
        } else if (isUncertainWrite(error)) {
          // Keep the original body, revision and key; the commit may have landed.
          setPending(intent);
          setWriteError('提交结果未知，已保留原请求，请用同一幂等键重试确认。');
        } else {
          setPending(undefined);
          setWriteError(error.message);
        }
      },
    );
  };

  const submit = () => {
    const built = buildRetentionInput(form);
    if (!built.input) {
      setFormError(built.errors.join(' '));
      return;
    }
    setFormError('');
    const expectedRevision = current?.revision ?? 0;
    const intent: PendingIntent = pending
      ? { ...pending, environment: form.environment, retentionDays: built.input.retention_days }
      : {
          revision: expectedRevision,
          environment: built.input.environment,
          retentionDays: built.input.retention_days,
          key: crypto.randomUUID(),
        };
    persist(intent);
  };

  const sweepColumns: DataTableColumn<RetentionSweep>[] = [
    {
      key: 'created_at',
      header: '执行时间',
      render: (row) => formatSweepInstant(row.created_at),
    },
    {
      key: 'mode',
      header: '模式',
      render: (row) => sweepModeLabel(row.dry_run),
    },
    {
      key: 'status',
      header: '状态',
      render: (row) => (
        <Badge tone={sweepStatusTone(row.status)}>{sweepStatusLabel(row.status)}</Badge>
      ),
    },
    {
      key: 'cutoff_at',
      header: '清理截止点',
      render: (row) => formatSweepInstant(row.cutoff_at),
    },
    {
      key: 'rows_candidates',
      header: '涉及行数',
      render: (row) => String(row.rows_candidates),
    },
    {
      key: 'detail',
      header: '说明',
      render: (row) => row.detail || '—',
    },
  ];

  return (
    <>
      <PageHeading title="数据保留" />
      <div className="retention-intro inline-notice">
        <Alert
          tone="info"
          title="保留策略定义事实数据的可查询时间窗口。当前页面只管理策略配置；按窗口实际回收历史数据由独立的清理执行器负责，且不会削弱数据删除（erasure）的复活屏障。"
        />
      </div>

      <DataSurface label="保留策略">
        {policy.isError ? (
          <Alert
            tone="error"
            title="无法读取保留策略"
            action={<Button onClick={() => void policy.refetch()}>重试</Button>}
          >
            {policy.error instanceof ApiError ? policy.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <form
            className="console-form retention-form"
            onSubmit={(event) => {
              event.preventDefault();
              if (!saving && (dirty || pending)) submit();
            }}
          >
            <div className="retention-fields">
              <Field label="环境" htmlFor="retention-environment">
                <Select
                  id="retention-environment"
                  value={form.environment}
                  options={environmentOptions}
                  disabled={saving}
                  onChange={(event) => {
                    const next = event.target.value as RetentionEnvironment;
                    setSelectedEnv(next);
                    setForm((currentForm) => ({ ...currentForm, environment: next }));
                  }}
                />
              </Field>
              <Field
                label="保留天数"
                htmlFor="retention-days"
                hint={`范围 ${RETENTION_MIN_DAYS}–${RETENTION_MAX_DAYS} 天的整数。`}
                error={formError}
                required
              >
                <Input
                  id="retention-days"
                  value={form.retentionDays}
                  inputMode="numeric"
                  autoComplete="off"
                  maxLength={4}
                  placeholder={`${RETENTION_MIN_DAYS}–${RETENTION_MAX_DAYS}`}
                  disabled={saving}
                  onChange={(event) => {
                    setForm((currentForm) => ({
                      ...currentForm,
                      retentionDays: event.target.value,
                    }));
                    setFormError('');
                    setWriteError('');
                  }}
                />
              </Field>
            </div>

            <div className="retention-summary">
              <p className="evia-field-hint">{retentionSummary(current?.retention_days)}</p>
              <p className="evia-field-hint">
                当前修订版本：{current?.revision ?? 0}
                {configured
                  ? `；最近更新：${formatPolicyInstant(current?.updated_at)}`
                  : '；该环境尚未配置策略。'}
              </p>
            </div>

            {writeError && (
              <div className="inline-notice">
                <Alert tone="error" title={writeError} />
              </div>
            )}

            <div className="inline-actions retention-submit">
              <Button
                type="submit"
                variant="primary"
                loading={saving}
                disabled={!dirty && !pending}
              >
                {pending ? '用原幂等键重试' : configured ? '更新保留策略' : '创建保留策略'}
              </Button>
              <span className="evia-field-hint">
                保存需要 If-Match 修订与幂等键；修订冲突会自动重新加载。
              </span>
            </div>
          </form>
        )}
      </DataSurface>

      <DataSurface label="清理历史">
        {sweeps.isError ? (
          <Alert
            tone="error"
            title="无法读取清理历史"
            action={<Button onClick={() => void sweeps.refetch()}>重试</Button>}
          >
            {sweeps.error instanceof ApiError ? sweeps.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <DataTable
            caption="数据保留清理执行历史"
            columns={sweepColumns}
            rows={sweeps.data?.items ?? []}
            rowKey={(row) => row.sweep_id}
            minWidth={640}
            loading={sweeps.isFetching}
            empty={
              <EmptyPanel description="还没有清理记录；retention-worker 运行后会在此显示演练或强制执行结果。" />
            }
          />
        )}
      </DataSurface>
    </>
  );
}
