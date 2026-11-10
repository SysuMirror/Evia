import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Project,
  type QueryJob,
  type QueryResult,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  Select,
  type DataTableColumn,
} from '@evia/design-system';
import {
  buildRunExecute,
  defaultRunForm,
  MAX_GROUP_BY,
  type GroupByInput,
  type MetricRunForm,
} from './model';

interface RunPanelProps {
  project: Project;
  session: Session;
  metricID: string;
  version: number;
  canRun: boolean;
}

type JobState = QueryJob['state'];

export function MetricRunPanel({ project, session, metricID, version, canRun }: RunPanelProps) {
  const [form, setForm] = useState<MetricRunForm>(() => defaultRunForm(project.timezone));
  const [error, setError] = useState('');
  const [job, setJob] = useState<QueryJob>();
  const [result, setResult] = useState<QueryResult>();
  const [polling, setPolling] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // Group-by fields may only reference events the published metric pins.
  const metricVersion = useQuery({
    queryKey: ['metrics', project.id, session.user_id, metricID, 'version', version],
    queryFn: ({ signal }) => api.getMetricVersion(project.id, metricID, version, signal),
    enabled: version >= 1,
  });
  const pinnedEvents = metricVersion.data?.definition.event_versions ?? [];

  const updateGroupBy = (index: number, changes: Partial<GroupByInput>) =>
    setForm((current) => ({
      ...current,
      groupBy: current.groupBy.map((entry, currentIndex) =>
        currentIndex === index ? { ...entry, ...changes } : entry,
      ),
    }));
  const addGroupBy = () => {
    const first = pinnedEvents[0];
    if (!first || form.groupBy.length >= MAX_GROUP_BY) return;
    setForm((current) => ({
      ...current,
      groupBy: [
        ...current.groupBy,
        { event_key: first.event_key, schema_version: String(first.schema_version), key: '' },
      ],
    }));
  };
  const removeGroupBy = (index: number) =>
    setForm((current) => ({
      ...current,
      groupBy: current.groupBy.filter((_, currentIndex) => currentIndex !== index),
    }));

  useEffect(() => () => clearTimeout(timer.current), []);

  const stopPolling = () => {
    clearTimeout(timer.current);
    setPolling(false);
  };

  const poll = (queryID: string) => {
    setPolling(true);
    const tick = async () => {
      try {
        const current = await api.getQuery(project.id, queryID);
        setJob(current);
        if (current.state === 'succeeded') {
          setResult(await api.getQueryResult(project.id, queryID));
          stopPolling();
          return;
        }
        if (current.state === 'failed' || current.state === 'canceled') {
          stopPolling();
          return;
        }
        timer.current = setTimeout(tick, 700);
      } catch (caught) {
        stopPolling();
        setError(caught instanceof ApiError ? caught.message : '查询轮询失败，请重试。');
      }
    };
    void tick();
  };

  const run = () => {
    setError('');
    setResult(undefined);
    const { execute, error: buildError } = buildRunExecute(form);
    if (buildError || !execute) {
      setError(buildError ?? '运行参数无效。');
      return;
    }
    void (async () => {
      try {
        const queued = await api.executeMetricVersion(
          project.id,
          metricID,
          version,
          execute as never,
          crypto.randomUUID(),
          session.csrf_token,
        );
        setJob(queued);
        poll(queued.query_id);
      } catch (caught) {
        setError(caught instanceof ApiError ? caught.message : '执行入队失败。');
      }
    })();
  };

  const cancel = async () => {
    if (!job) return;
    try {
      const cancelled = await api.cancelQuery(project.id, job.query_id, session.csrf_token);
      setJob(cancelled);
      stopPolling();
    } catch (caught) {
      setError(caught instanceof ApiError ? caught.message : '取消失败。');
    }
  };

  const columns: DataTableColumn<Record<string, unknown>>[] = (result?.columns ?? []).map(
    (column) => ({
      key: column.key,
      header: column.label || column.key,
      render: (row) => {
        const value = row[column.key];
        return value === null || value === undefined ? '—' : String(value);
      },
    }),
  );

  return (
    <DataSurface label="指标运行">
      {version < 1 ? (
        <EmptyPanel description="该指标还没有已发布版本，无法运行。" />
      ) : (
        <>
          <div className="metric-fields">
            <Field label="开始时间" htmlFor="metric-run-from">
              <Input
                id="metric-run-from"
                type="datetime-local"
                value={form.from}
                max={form.to}
                onChange={(event) => setForm((c) => ({ ...c, from: event.target.value }))}
              />
            </Field>
            <Field label="结束时间" htmlFor="metric-run-to">
              <Input
                id="metric-run-to"
                type="datetime-local"
                value={form.to}
                min={form.from}
                onChange={(event) => setForm((c) => ({ ...c, to: event.target.value }))}
              />
            </Field>
            <Field label="时区" htmlFor="metric-run-tz">
              <Input
                id="metric-run-tz"
                value={form.timezone}
                onChange={(event) => setForm((c) => ({ ...c, timezone: event.target.value }))}
              />
            </Field>
            <Field label="分桶间隔" htmlFor="metric-run-interval">
              <Select
                id="metric-run-interval"
                value={form.interval}
                options={[
                  { value: '', label: '整段聚合' },
                  { value: 'minute', label: '分钟' },
                  { value: 'hour', label: '小时' },
                  { value: 'day', label: '天' },
                ]}
                onChange={(event) =>
                  setForm((c) => ({
                    ...c,
                    interval: event.target.value as MetricRunForm['interval'],
                  }))
                }
              />
            </Field>
            <Field label="行数上限" htmlFor="metric-run-limit">
              <Input
                id="metric-run-limit"
                value={form.limit}
                inputMode="numeric"
                onChange={(event) => setForm((c) => ({ ...c, limit: event.target.value }))}
              />
            </Field>
          </div>
          <Field label="运行筛选（可选，与固定筛选取 AND）" htmlFor="metric-run-filter">
            <Input
              id="metric-run-filter"
              value={form.filterJSON}
              placeholder='{"op":"eq",...}'
              onChange={(event) => setForm((c) => ({ ...c, filterJSON: event.target.value }))}
            />
          </Field>

          <fieldset className="metric-fieldset">
            <legend>分组维度（可选，最多 {MAX_GROUP_BY} 个，只能引用已固定事件的属性）</legend>
            {form.groupBy.map((entry, index) => (
              <div className="metric-groupby-row" key={index}>
                <Select
                  aria-label={`分组 ${index + 1} 事件`}
                  value={`${entry.event_key}@${entry.schema_version}`}
                  options={pinnedEvents.map((event) => ({
                    value: `${event.event_key}@${event.schema_version}`,
                    label: `${event.event_key} v${event.schema_version}`,
                  }))}
                  onChange={(event) => {
                    const [eventKey, schemaVersion] = event.target.value.split('@');
                    updateGroupBy(index, {
                      event_key: eventKey,
                      schema_version: schemaVersion,
                    });
                  }}
                />
                <Input
                  aria-label={`分组 ${index + 1} 属性名`}
                  placeholder="属性 key，如 page"
                  value={entry.key}
                  onChange={(event) => updateGroupBy(index, { key: event.target.value })}
                />
                <Button variant="ghost" size="small" onClick={() => removeGroupBy(index)}>
                  移除
                </Button>
              </div>
            ))}
            <Button
              size="small"
              onClick={addGroupBy}
              disabled={form.groupBy.length >= MAX_GROUP_BY || pinnedEvents.length === 0}
            >
              添加分组
            </Button>
          </fieldset>
          {error && (
            <div className="inline-notice">
              <Alert tone="error" title={error} />
            </div>
          )}
          <div className="inline-actions">
            <Button variant="primary" onClick={run} disabled={!canRun || polling}>
              运行 v{version}
            </Button>
            {job && (job.state === 'queued' || job.state === 'running') && (
              <Button variant="danger" onClick={() => void cancel()}>
                取消任务
              </Button>
            )}
            {job && <JobBadge state={job.state} />}
          </div>

          {result && (
            <div className="metric-result">
              <p className="evia-field-hint">
                精度 {result.metadata.accuracy}，覆盖 {result.metadata.coverage.state}
                {result.metadata.coverage.known_gaps.length > 0
                  ? `，已知缺口 ${result.metadata.coverage.known_gaps.length} 项`
                  : ''}
              </p>
              <DataTable
                caption="指标运行结果"
                columns={columns}
                rows={result.rows as Record<string, unknown>[]}
                rowKey={(row) => JSON.stringify(row)}
                minWidth={480}
                empty={<EmptyPanel description="范围内没有符合条件的事实。" />}
              />
            </div>
          )}
        </>
      )}
    </DataSurface>
  );
}

function JobBadge({ state }: { state: JobState }) {
  const tone =
    state === 'succeeded'
      ? 'success'
      : state === 'failed' || state === 'canceled'
        ? 'neutral'
        : 'info';
  const labels: Record<JobState, string> = {
    queued: '排队中',
    running: '运行中',
    succeeded: '已完成',
    failed: '失败',
    canceled: '已取消',
    expired: '已过期',
  };
  return <span className={`evia-badge evia-badge-${tone}`}>{labels[state]}</span>;
}
