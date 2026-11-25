import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type CollectionQuota,
  type Environment,
  type Project,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  Field,
  Input,
  Select,
  useToast,
} from '@evia/design-system';
import {
  defaultQuotaForm,
  QUOTA_BURST_MAX,
  QUOTA_BURST_MIN,
  QUOTA_RATE_MAX,
  QUOTA_RATE_MIN,
  quotaSourceLabel,
  runtimeStatusLabel,
  validateQuotaForm,
  type QuotaFormState,
} from './model';

type Props = { project: Project; session: Session };

const ENVIRONMENTS: Environment[] = ['development', 'staging', 'production'];

export function QuotaPanel({ project, session }: Props) {
  const cache = useQueryClient();
  const toast = useToast();
  const owner = project.permissions.includes('settings.write');
  const [environment, setEnvironment] = useState<Environment>('development');
  const [form, setForm] = useState<QuotaFormState>(defaultQuotaForm());
  const [formErrors, setFormErrors] = useState<string[]>([]);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);

  const quotaKey = ['collection-quota', project.id, environment] as const;
  const quota = useQuery({
    queryKey: quotaKey,
    queryFn: ({ signal }) => api.getCollectionQuota(project.id, environment, signal),
  });

  useEffect(() => {
    if (quota.data) {
      setForm({ rate: String(quota.data.rate_per_second), burst: String(quota.data.burst) });
      setFormErrors([]);
    }
  }, [quota.data]);

  const submit = async () => {
    const errors = validateQuotaForm(form);
    if (errors.length > 0 || !quota.data) {
      setFormErrors(errors.length ? errors : ['请等待当前策略加载完成。']);
      return;
    }
    setBusy(true);
    setNotice('');
    try {
      const result = await api.patchCollectionQuota(
        project.id,
        {
          environment,
          rate_per_second: Number(form.rate),
          burst: Number(form.burst),
        },
        quota.data.revision,
        crypto.randomUUID(),
        session.csrf_token,
      );
      void cache.invalidateQueries({ queryKey: quotaKey });
      void cache.invalidateQueries({ queryKey: ['diagnostics', project.id] });
      if (result.runtime_status === 'bucket_reset_pending') {
        setNotice('配额已持久化，但 Redis 桶重置失败；在旧桶过期前该环境接收可能临时返回 503。');
      } else {
        toast.success(`${environment} 采集配额已发布并生效。`);
      }
    } catch (error) {
      setNotice(error instanceof ApiError ? error.message : '配额发布失败，请稍后重试。');
    } finally {
      setBusy(false);
    }
  };

  return (
    <DataSurface label="采集配额管理">
      <div className="diagnostics-section-head">
        <h2>采集配额管理</h2>
      </div>
      {quota.isError ? (
        <Alert
          tone="error"
          title="无法读取采集配额"
          action={<Button onClick={() => void quota.refetch()}>重试</Button>}
        >
          {quota.error instanceof ApiError ? quota.error.message : '请稍后再试。'}
        </Alert>
      ) : (
        <>
          <Field label="环境" htmlFor="quota-environment">
            <Select
              id="quota-environment"
              value={environment}
              options={ENVIRONMENTS.map((value) => ({ value, label: value }))}
              disabled={busy}
              onChange={(event) => setEnvironment(event.target.value as Environment)}
            />
          </Field>
          {quota.data ? (
            <QuotaDetail quota={quota.data} />
          ) : (
            <p className="evia-field-hint">正在加载有效策略。</p>
          )}
          {owner ? (
            <form
              className="console-form"
              onSubmit={(event) => {
                event.preventDefault();
                if (!busy) void submit();
              }}
            >
              <div className="quota-fields">
                <Field
                  label="速率（事件/秒）"
                  htmlFor="quota-rate"
                  hint={`${QUOTA_RATE_MIN} 到 ${QUOTA_RATE_MAX}`}
                  error={formErrors.find((value) => value.includes('速率'))}
                >
                  <Input
                    id="quota-rate"
                    value={form.rate}
                    inputMode="decimal"
                    disabled={busy}
                    onChange={(event) =>
                      setForm((current) => ({ ...current, rate: event.target.value }))
                    }
                  />
                </Field>
                <Field
                  label="突发容量"
                  htmlFor="quota-burst"
                  hint={`${QUOTA_BURST_MIN} 到 ${QUOTA_BURST_MAX}`}
                  error={formErrors.find((value) => value.includes('突发容量'))}
                >
                  <Input
                    id="quota-burst"
                    value={form.burst}
                    inputMode="numeric"
                    disabled={busy}
                    onChange={(event) =>
                      setForm((current) => ({ ...current, burst: event.target.value }))
                    }
                  />
                </Field>
              </div>
              {formErrors.length > 0 ? <Alert tone="error" title={formErrors.join(' ')} /> : null}
              {notice ? <Alert tone="warning" title={notice} /> : null}
              <div className="quota-actions">
                <Button type="submit" loading={busy} disabled={busy || quota.isFetching}>
                  发布配额覆盖
                </Button>
                <span className="evia-field-hint">
                  修订 {quota.data?.revision ?? 0}；覆盖立即发布到运行时限流器并重置该环境 Redis
                  桶。
                </span>
              </div>
            </form>
          ) : (
            <Alert tone="info" title="仅项目所有者可以修改采集配额，当前为只读视图。" />
          )}
        </>
      )}
    </DataSurface>
  );
}

function QuotaDetail({ quota }: { quota: CollectionQuota }) {
  return (
    <dl className="quota-detail">
      <div>
        <dt>策略来源</dt>
        <dd>
          <Badge tone={quota.source === 'project_override' ? 'success' : 'neutral'}>
            {quotaSourceLabel(quota.source)}
          </Badge>
        </dd>
      </div>
      <div>
        <dt>有效速率</dt>
        <dd>{quota.rate_per_second} 事件/秒</dd>
      </div>
      <div>
        <dt>有效突发容量</dt>
        <dd>{quota.burst}</dd>
      </div>
      <div>
        <dt>运行时状态</dt>
        <dd>{runtimeStatusLabel(quota.runtime_status)}</dd>
      </div>
    </dl>
  );
}
