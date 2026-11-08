import {
  Component,
  lazy,
  Suspense,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type QueryJob,
  type QueryResult,
  type SavedAnalysisExecute,
  type SavedAnalysisDefinition,
  type SavedAnalysisVersion,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  CopyButton,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  Panel,
  ResultHeader,
  Select,
  type DataTableColumn,
} from '@evia/design-system';
import { initialForm, formatTime } from './analysis-model';
import { CatalogError, environmentLabel } from './catalog-helpers';
import { useCatalogScope } from './catalog-scope';
import type { SavedAnalysisChartProps } from '@evia/design-system/charts';
import { savedFieldLabel } from './saved-analysis-model';

const SavedChart = lazy(() =>
  import('@evia/design-system/charts').then((module) => ({ default: module.SavedAnalysisChart })),
);
class ChartBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? (
      <p role="status">图表暂时无法加载，完整结果保留在下方表格。</p>
    ) : (
      this.props.children
    );
  }
}
type ExecutionAttempt = { key: string; version: SavedAnalysisVersion; body: SavedAnalysisExecute };
type ExecutionSnapshot = ExecutionAttempt & { job: QueryJob; result?: QueryResult };
export type SavedRunWork = { dirty: boolean; busy: boolean; retryPending?: boolean };
const jobLabels: Record<QueryJob['state'], string> = {
  queued: '排队中',
  running: '执行中',
  succeeded: '已完成',
  failed: '执行失败',
  canceled: '已取消',
  expired: '已过期',
};
const active = (job?: QueryJob) => job?.state === 'queued' || job?.state === 'running';
const uncertain = (error: unknown) =>
  error instanceof ApiError && (error.status === 0 || error.status >= 500 || error.status === 408);
const zones = [...new Set(['UTC', 'Asia/Shanghai', ...Intl.supportedValuesOf('timeZone')])];
function displayCell(value: unknown, type: string, timezone: string) {
  if (value === undefined) return <span>缺失</span>;
  if (value === null) return <span>null</span>;
  if (type === 'datetime')
    return <time dateTime={String(value)}>{formatTime(value, timezone)}</time>;
  if (typeof value === 'boolean') return String(value);
  if (typeof value === 'number')
    return Number.isSafeInteger(value) ? value.toLocaleString('zh-CN') : String(value);
  if (typeof value === 'object')
    return (
      <details>
        <summary>查看内容</summary>
        <CodeBlock>{JSON.stringify(value, null, 2)}</CodeBlock>
      </details>
    );
  return String(value);
}
function metricInfo(definition: SavedAnalysisDefinition, index: number) {
  const metric = definition.query.metrics?.[index];
  if (!metric) return { label: `指标 ${index + 1}`, unit: undefined };
  const field = metric.field;
  const operation =
    (
      {
        count: '事件数',
        unique: field
          ? '去重值数'
          : `${definition.query.subject === 'anonymous' ? '匿名标识' : '原始 UID'} 去重数`,
        sum: '求和',
        avg: '平均值',
        min: '最小值',
        max: '最大值',
        p95: 'P95',
      } as Record<string, string>
    )[metric.op] ?? metric.op;
  return {
    label: `${operation}${field ? `（${savedFieldLabel(field)}）` : ''}${metric.accuracy === 'approximate' ? '，近似' : ''}`,
    unit: metric.op === 'count' ? '次' : metric.op === 'unique' && !field ? '主体' : undefined,
  };
}

export function savedAnalysisResultChart(
  version: SavedAnalysisVersion,
  result: QueryResult,
  execution: SavedAnalysisExecute,
): SavedAnalysisChartProps | undefined {
  const definition = version.definition,
    kind = definition.visualization.kind;
  if (kind === 'metric' || kind === 'table') return undefined;
  const finite = (value: unknown) =>
    typeof value === 'number' && Number.isFinite(value) ? value : null;
  if (kind === 'funnel') {
    const rows = [...result.rows].sort(
      (left, right) => Number(left.step_index) - Number(right.step_index),
    );
    return {
      kind,
      label: definition.title,
      timezone: execution.timezone,
      categories: rows.map((row, index) => {
        const step = definition.query.funnel?.steps[Number(row.step_index)];
        return {
          key: String(row.step_index),
          label: `${Number(row.step_index) + 1}. ${step?.event_key ?? String(row.event_key ?? '')}${step ? ` v${step.schema_version}` : ''}`,
        };
      }),
      series: [
        {
          id: 'subjects',
          label: '到达主体数',
          unit: '主体',
          values: rows.map((row) => finite(row.subjects)),
        },
      ],
    };
  }
  const metricColumns = result.columns.filter((column) => /^metric_\d+$/.test(column.key));
  const groupColumns = result.columns.filter((column) => /^group_\d+$/.test(column.key));
  const group = (row: Record<string, unknown>) =>
    groupColumns.map((column) => {
      const present = row[`${column.key}_present`],
        value = row[column.key];
      const label = present === false || value === undefined ? '缺失' : JSON.stringify(value);
      const field = definition.query.group_by?.[Number(column.key.slice(6))];
      return {
        key: [column.key, present === false ? 'missing' : 'present', typeof value, value],
        valueLabel: label,
        label: `${field ? savedFieldLabel(field) : (column.label ?? column.key)}=${label}`,
      };
    });
  const groupKey = (row: Record<string, unknown>) =>
    JSON.stringify(group(row).map((item) => item.key));
  const groupLabel = (row: Record<string, unknown>) => {
    const fields = group(row);
    return fields.length === 1
      ? fields[0]!.valueLabel
      : fields.map((item) => item.label).join('，');
  };
  if (definition.query.interval) {
    const times = [...new Set(result.rows.map((row) => String(row.bucket)))].sort(
      (left, right) => Date.parse(left) - Date.parse(right),
    );
    const groups = new Map<string, { label: string; rows: Record<string, unknown>[] }>();
    for (const row of result.rows) {
      const key = groupKey(row);
      const item = groups.get(key) ?? { label: groupLabel(row), rows: [] };
      item.rows.push(row);
      groups.set(key, item);
    }
    const series: SavedAnalysisChartProps['series'][number][] = [];
    for (const [groupID, item] of groups)
      for (const metric of metricColumns) {
        const values = new Map(
          item.rows.map((row) => [String(row.bucket), finite(row[metric.key])]),
        );
        const info = metricInfo(definition, Number(metric.key.slice(7)));
        series.push({
          id: `${groupID}:${metric.key}`,
          label: `${item.label ? `${item.label}，` : ''}${info.label}`,
          unit: info.unit,
          values: times.map((time) => values.get(time) ?? null),
        });
      }
    return {
      kind: kind === 'bar' ? 'bar' : 'line',
      label: definition.title,
      timezone: execution.timezone,
      categories: times.map((time) => ({
        key: time,
        label: formatTime(time, execution.timezone),
        timestamp: Date.parse(time),
      })),
      series,
      timeRange: {
        from: Date.parse(execution.time_range.from),
        to: Date.parse(execution.time_range.to),
      },
      intervalMs: { minute: 60000, hour: 3600000, day: 86400000 }[definition.query.interval],
    };
  }
  return {
    kind: 'bar',
    label: definition.title,
    timezone: execution.timezone,
    categories: result.rows.map((row, index) => ({
      key: groupKey(row) || String(index),
      label: groupLabel(row) || '整段范围',
    })),
    series: metricColumns.map((metric) => ({
      ...metricInfo(definition, Number(metric.key.slice(7))),
      id: metric.key,
      values: result.rows.map((row) => finite(row[metric.key])),
    })),
  };
}

export function SavedAnalysisRun({
  version,
  project,
  environment,
  session,
  onWorkChange,
  onAccessDenied,
  disabled = false,
}: {
  version: SavedAnalysisVersion;
  project: Project;
  environment: Environment;
  session: Session;
  onWorkChange: (work: SavedRunWork) => void;
  onAccessDenied: (error: Error) => void;
  disabled?: boolean;
}) {
  const id = useId();
  const cache = useQueryClient();
  const [range, setRange] = useState(() => {
    const form = initialForm(project.timezone);
    return { from: form.from, to: form.to, timezone: project.timezone };
  });
  const [rangeBaseline, setRangeBaseline] = useState(() => JSON.stringify(range));
  const [rangeOpen, setRangeOpen] = useState(false);
  const [rangeError, setRangeError] = useState('');
  const [attempt, setAttempt] = useState<ExecutionAttempt>();
  const [snapshot, setSnapshot] = useState<ExecutionSnapshot>();
  const [error, setError] = useState<Error | null>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [observing, setObserving] = useState(false);
  const [denied, setDenied] = useState(false);
  const operation = useRef(false);
  const request = useRef<AbortController | null>(null);
  const polling = useRef<AbortController | null>(null);
  const identity = JSON.stringify([
    project.id,
    environment,
    session.user_id,
    session.csrf_token,
    [...project.permissions].sort(),
    version.analysis_id,
  ]);
  const scope = useCatalogScope(identity, !denied, { project, session });
  const workDirty = !!attempt || active(snapshot?.job) || JSON.stringify(range) !== rangeBaseline;
  useEffect(() => {
    onWorkChange({ dirty: workDirty, busy, retryPending: !!attempt });
  }, [onWorkChange, workDirty, busy, attempt]);
  useEffect(
    () => () => {
      onWorkChange({ dirty: false, busy: false });
    },
    [onWorkChange],
  );
  useEffect(() => {
    setSnapshot(undefined);
    setAttempt(undefined);
    setError(null);
    setNotice('');
    setDenied(false);
    setBusy(false);
    setObserving(false);
    setRangeOpen(false);
    operation.current = false;
    return () => {
      request.current?.abort();
      polling.current?.abort();
    };
  }, [identity]);
  function fail(failure: unknown) {
    const next = failure instanceof Error ? failure : new Error('请求失败，请重试。');
    if (failure instanceof ApiError && [401, 403].includes(failure.status)) {
      scope.invalidate();
      setDenied(true);
      setSnapshot(undefined);
      setAttempt(undefined);
      polling.current?.abort();
      setObserving(false);
      cache.removeQueries({
        queryKey: ['saved-analyses', project.id, environment, session.user_id],
      });
      onAccessDenied(next);
    }
    if (failure instanceof ApiError && failure.code === 'QUERY_EXPIRED') {
      setSnapshot((previous) =>
        previous
          ? { ...previous, job: { ...previous.job, state: 'expired' }, result: undefined }
          : previous,
      );
      setObserving(false);
    }
    setError(next);
  }
  function rangeBody(): SavedAnalysisExecute {
    const from = new Date(`${range.from}Z`),
      to = new Date(`${range.to}Z`);
    const maximum = version.definition.query.kind === 'funnel' ? 7 : 31;
    if (
      !Number.isFinite(from.getTime()) ||
      !Number.isFinite(to.getTime()) ||
      from >= to ||
      to.getTime() - from.getTime() > maximum * 86400000 ||
      from.getUTCFullYear() < 1970 ||
      to.getUTCFullYear() > 2100
    )
      throw new Error(
        `请选择开始早于结束的有效时间范围，最多 ${maximum} 天，年份为 1970 至 2100。`,
      );
    try {
      new Intl.DateTimeFormat('zh-CN', { timeZone: range.timezone });
    } catch {
      throw new Error('请选择有效的 IANA 时区。');
    }
    return {
      time_range: { from: from.toISOString(), to: to.toISOString() },
      timezone: range.timezone,
    };
  }
  async function execute() {
    if (
      disabled ||
      operation.current ||
      active(snapshot?.job) ||
      denied ||
      version.environment !== environment
    )
      return;
    let current = attempt;
    if (!current) {
      try {
        current = {
          key: crypto.randomUUID(),
          version: structuredClone(version),
          body: rangeBody(),
        };
      } catch (failure) {
        setRangeError((failure as Error).message);
        setRangeOpen(true);
        return;
      }
    }
    const token = scope.capture(),
      controller = new AbortController();
    operation.current = true;
    request.current = controller;
    setBusy(true);
    setError(null);
    setAttempt(current);
    setNotice('');
    try {
      const job = await api.executeSavedAnalysisVersion(
        project.id,
        current.version.analysis_id,
        current.version.version,
        current.body,
        current.key,
        session.csrf_token,
        controller.signal,
      );
      if (!scope.isCurrent(token) || controller.signal.aborted) return;
      polling.current?.abort();
      setSnapshot({ ...current, job });
      setRangeBaseline(
        JSON.stringify({
          from: current.body.time_range.from.slice(0, 16),
          to: current.body.time_range.to.slice(0, 16),
          timezone: current.body.timezone,
        }),
      );
      setAttempt(undefined);
      setObserving(true);
    } catch (failure) {
      if (scope.isCurrent(token) && !controller.signal.aborted) {
        if (!uncertain(failure)) setAttempt(undefined);
        fail(failure);
      }
    } finally {
      if (scope.isCurrent(token) && !controller.signal.aborted) {
        operation.current = false;
        setBusy(false);
      }
    }
  }
  async function cancel() {
    if (operation.current || !snapshot || !active(snapshot.job)) return;
    const token = scope.capture(),
      controller = new AbortController(),
      queryID = snapshot.job.query_id;
    operation.current = true;
    request.current = controller;
    polling.current?.abort();
    setObserving(false);
    setBusy(true);
    setError(null);
    try {
      const job = await api.cancelQuery(project.id, queryID, session.csrf_token, controller.signal);
      if (!scope.isCurrent(token) || controller.signal.aborted) return;
      setSnapshot((previous) =>
        previous?.job.query_id === queryID ? { ...previous, job } : previous,
      );
      setNotice(
        job.state === 'canceled' ? '查询已取消。' : '服务端已处理取消请求，以下状态以服务端为准。',
      );
      if (active(job) || job.state === 'succeeded') setObserving(true);
    } catch (failure) {
      if (scope.isCurrent(token) && !controller.signal.aborted) fail(failure);
    } finally {
      if (scope.isCurrent(token) && !controller.signal.aborted) {
        operation.current = false;
        setBusy(false);
      }
    }
  }
  useEffect(() => {
    if (!observing || !snapshot || denied) return;
    const controller = new AbortController(),
      token = scope.capture(),
      queryID = snapshot.job.query_id;
    polling.current = controller;
    const current = () => !controller.signal.aborted && scope.isCurrent(token);
    const wait = (milliseconds: number) =>
      new Promise<void>((resolve, reject) => {
        const abort = () => {
          clearTimeout(timer);
          reject(new DOMException('Aborted', 'AbortError'));
        };
        const timer = window.setTimeout(() => {
          controller.signal.removeEventListener('abort', abort);
          resolve();
        }, milliseconds);
        controller.signal.addEventListener('abort', abort, { once: true });
      });
    void (async () => {
      let job = snapshot.job;
      const deadline = Date.now() + 12 * 60 * 1000;
      try {
        while (current()) {
          if (job.state === 'succeeded') {
            const result = await api.getQueryResult(project.id, queryID, controller.signal);
            if (current()) {
              setSnapshot((previous) =>
                previous?.job.query_id === queryID ? { ...previous, job, result } : previous,
              );
              setObserving(false);
            }
            return;
          }
          if (!active(job)) {
            if (current()) setObserving(false);
            return;
          }
          if (Date.now() >= deadline) {
            setObserving(false);
            setNotice('已暂停自动检查，服务端查询可能仍在执行，可继续检查或取消。');
            return;
          }
          await wait(Math.max(250, job.poll_after_ms ?? 1000));
          if (!current()) return;
          job = await api.getQuery(project.id, queryID, controller.signal);
          if (current())
            setSnapshot((previous) =>
              previous?.job.query_id === queryID ? { ...previous, job } : previous,
            );
        }
      } catch (failure) {
        if (current()) {
          fail(failure);
          setObserving(false);
        }
      }
    })();
    return () => controller.abort();
  }, [observing, snapshot?.job.query_id, identity, denied]);
  const result = snapshot?.result,
    rows = result?.rows ?? [];
  const metricName = (key: string) =>
    snapshot ? metricInfo(snapshot.version.definition, Number(key.slice(7))).label : key;
  const groupName = (key: string) => {
    const index = Number(key.match(/^group_(\d+)/)?.[1]);
    const field = snapshot?.version.definition.query.group_by?.[index];
    return field ? `${savedFieldLabel(field)}${key.endsWith('_present') ? '，字段存在' : ''}` : key;
  };
  const columns: DataTableColumn<Record<string, unknown>>[] =
    result?.columns.map((column) => ({
      key: column.key,
      header: column.key.startsWith('metric_')
        ? metricName(column.key)
        : column.key.startsWith('group_')
          ? groupName(column.key)
          : ({
              bucket: '时间',
              event_key: '事件',
              step_index: '步骤',
              subjects: '到达主体数',
              conversion_rate: '相对首步转化率',
            }[column.key] ??
            column.label ??
            column.key),
      render: (row) =>
        column.key === 'conversion_rate' && typeof row[column.key] === 'number' ? (
          <span title={`原始比例 ${String(row[column.key])}`}>
            {((row[column.key] as number) * 100).toLocaleString('zh-CN', {
              maximumSignificantDigits: 21,
            })}
            %
          </span>
        ) : column.key === 'step_index' && typeof row[column.key] === 'number' ? (
          String((row[column.key] as number) + 1)
        ) : (
          displayCell(row[column.key], column.type, snapshot!.body.timezone)
        ),
    })) ?? [];
  const chart = useMemo(
    () =>
      snapshot && result
        ? savedAnalysisResultChart(snapshot.version, result, snapshot.body)
        : undefined,
    [snapshot?.version, result, snapshot?.body],
  );
  const changed =
    snapshot &&
    (snapshot.version.version !== version.version ||
      Date.parse(snapshot.body.time_range.from) !== Date.parse(`${range.from}Z`) ||
      Date.parse(snapshot.body.time_range.to) !== Date.parse(`${range.to}Z`) ||
      snapshot.body.timezone !== range.timezone);
  if (denied) return <CatalogError error={error} />;
  return (
    <div className="saved-analysis-run section-stack">
      <Panel label="执行已发布分析" className="saved-analysis-run-controls">
        <form
          className="saved-analysis-run-form"
          onSubmit={(event) => {
            event.preventDefault();
            void execute();
          }}
        >
          <div className="analysis-query-footer">
            <div className="saved-analysis-run-context">
              <strong>
                运行 v{version.version}，{environmentLabel[version.environment]}
              </strong>
              <div className="analysis-query-summary">
                <span>
                  {range.from.replace('T', ' ')} 至 {range.to.replace('T', ' ')}（不含），UTC
                </span>
                <span>
                  显示时区 {range.timezone}，最多 {version.definition.query.limit ?? 200} 行
                </span>
              </div>
            </div>
            <div className="inline-actions">
              <Button
                onClick={() => setRangeOpen(true)}
                disabled={disabled || busy || !!attempt}
                aria-haspopup="dialog"
              >
                执行时间范围
              </Button>
              {active(snapshot?.job) && (
                <Button onClick={() => void cancel()} loading={busy}>
                  取消查询
                </Button>
              )}
              <Button
                type="submit"
                variant="primary"
                loading={busy}
                disabled={disabled || busy || active(snapshot?.job)}
              >
                {attempt ? '重试本次执行' : '运行已发布版本'}
              </Button>
            </div>
          </div>
        </form>
      </Panel>
      {attempt && !busy && (
        <Alert tone="warning" title="执行响应尚未确认">
          重试会复用 v{attempt.version.version}、原时间范围和同一请求标识，避免重复创建查询。
        </Alert>
      )}
      <CatalogError error={error} />
      {snapshot ? (
        <Panel label="已发布分析结果" className="saved-analysis-result">
          <ResultHeader
            title={snapshot.version.definition.title}
            summary={`v${snapshot.version.version}，${environmentLabel[snapshot.version.environment]}`}
            action={
              <div className="inline-actions">
                <Badge
                  tone={
                    snapshot.job.state === 'succeeded'
                      ? 'success'
                      : snapshot.job.state === 'failed' || snapshot.job.state === 'expired'
                        ? 'error'
                        : 'neutral'
                  }
                >
                  {jobLabels[snapshot.job.state]}
                </Badge>
                {result && (
                  <>
                    <Badge
                      tone={result.metadata.accuracy === 'approximate' ? 'warning' : 'neutral'}
                    >
                      {result.metadata.accuracy === 'exact'
                        ? '精确'
                        : result.metadata.accuracy === 'approximate'
                          ? '近似'
                          : '明细'}
                    </Badge>
                    <Badge
                      tone={result.metadata.coverage.state === 'settled' ? 'success' : 'warning'}
                    >
                      {
                        { provisional: '覆盖暂定', settled: '覆盖已稳定', partial: '部分覆盖' }[
                          result.metadata.coverage.state
                        ]
                      }
                    </Badge>
                  </>
                )}
              </div>
            }
          />
          <div className="section-stack">
            <div className="analysis-query-context">
              {formatTime(snapshot.body.time_range.from, snapshot.body.timezone)} 至{' '}
              {formatTime(snapshot.body.time_range.to, snapshot.body.timezone)}（不含），
              {snapshot.body.timezone}
            </div>
            {changed && (
              <Alert tone="warning" title="执行条件已修改">
                以下仍为 v{snapshot.version.version} 的上次结果，运行后更新。
              </Alert>
            )}
            {active(snapshot.job) && (
              <p className="field-help">
                离开会停止本页状态检查，服务端查询可能继续执行。需要停止时请先取消查询。
              </p>
            )}
            {(notice || observing) && (
              <p role="status">
                {notice ||
                  (snapshot.job.state === 'succeeded'
                    ? '正在获取结果。'
                    : '正在按服务端间隔检查状态。')}
              </p>
            )}
            {!observing &&
              (active(snapshot.job) || (snapshot.job.state === 'succeeded' && !result)) && (
                <Button
                  onClick={() => {
                    setError(null);
                    setObserving(true);
                  }}
                >
                  继续检查状态
                </Button>
              )}
            {snapshot.job.state === 'failed' && (
              <Alert tone="error" title="查询执行失败">
                {snapshot.job.error?.code}，
                {snapshot.job.error?.retryable ? '可保留条件重新运行。' : '请检查配置与时间范围。'}
                {snapshot.job.error?.field_paths?.join('，')}
              </Alert>
            )}
            {snapshot.job.state === 'canceled' && (
              <EmptyPanel description="查询已取消，可重新运行。" />
            )}
            {snapshot.job.state === 'expired' && (
              <EmptyPanel description="查询已过期，结果无法恢复，请重新运行。" />
            )}
            {result && (
              <>
                {snapshot.version.definition.visualization.kind === 'metric' && rows.length > 0 && (
                  <div className="analysis-stat">
                    <span>{metricName('metric_0')}</span>
                    <div className="metric-value">
                      {displayCell(rows[0]?.metric_0, 'number', snapshot.body.timezone)}
                    </div>
                  </div>
                )}
                {chart && rows.length > 0 && (
                  <ChartBoundary key={snapshot.job.query_id}>
                    <Suspense fallback={<LoadingPanel label="正在加载图表" />}>
                      <SavedChart {...chart} />
                    </Suspense>
                  </ChartBoundary>
                )}
                <DataSurface label="保存分析结果表">
                  <DataTable
                    caption="已发布分析的完整有界结果"
                    rows={rows}
                    columns={columns}
                    rowKey={(row) => String(rows.indexOf(row))}
                    empty="该范围内无匹配结果，不等同于数值零。"
                  />
                </DataSurface>
                <p className="field-help">
                  返回 {rows.length} 行，上限 {snapshot.version.definition.query.limit ?? 200}{' '}
                  行。缺失时间桶未补零，各桶去重数不能相加为整段去重数。
                </p>
                <details>
                  <summary>原始结果 JSON</summary>
                  <CodeBlock label="完整结果 JSON">{JSON.stringify(result, null, 2)}</CodeBlock>
                </details>
                <details>
                  <summary>精度、覆盖与生成信息</summary>
                  <dl className="detail-list">
                    <div>
                      <dt>采样率</dt>
                      <dd>{result.metadata.sample_rate * 100}%</dd>
                    </div>
                    <div>
                      <dt>覆盖范围</dt>
                      <dd>
                        {formatTime(
                          result.metadata.coverage.time_range.from,
                          snapshot.body.timezone,
                        )}{' '}
                        至{' '}
                        {formatTime(result.metadata.coverage.time_range.to, snapshot.body.timezone)}
                        （不含）
                      </dd>
                    </div>
                    <div>
                      <dt>已知缺口</dt>
                      <dd>
                        {result.metadata.coverage.known_gaps
                          .map(
                            (gap) =>
                              ({
                                pipeline_completeness_not_established: '尚未确认接入链路完整性',
                                identity_resolution_not_applied: '未应用身份关联',
                              })[gap] ?? gap,
                          )
                          .join('，') || '未报告'}
                      </dd>
                    </div>
                    <div>
                      <dt>已观察到的最晚接收</dt>
                      <dd>
                        {result.metadata.data_watermark
                          ? formatTime(result.metadata.data_watermark, snapshot.body.timezone)
                          : '无观测数据'}
                        ，不代表数据完整到达
                      </dd>
                    </div>
                    <div>
                      <dt>生成时间</dt>
                      <dd>
                        {formatTime(result.metadata.generated_at, snapshot.body.timezone)}，
                        {result.metadata.cache_hit ? '命中缓存' : '未命中缓存'}
                      </dd>
                    </div>
                  </dl>
                </details>
              </>
            )}
            <details>
              <summary>查询标识与执行快照</summary>
              <CopyButton value={snapshot.job.query_id} label="复制查询标识" />
              <CodeBlock>
                {JSON.stringify(
                  {
                    analysis_version: snapshot.version.version,
                    environment: snapshot.version.environment,
                    definition: snapshot.version.definition,
                    execution: snapshot.body,
                  },
                  null,
                  2,
                )}
              </CodeBlock>
            </details>
          </div>
        </Panel>
      ) : (
        <EmptyPanel description="选择执行范围并运行，结果将固定显示本次发布版本、环境与时间。" />
      )}
      <Modal
        open={rangeOpen}
        title="执行时间范围"
        onRequestClose={() => setRangeOpen(false)}
        footer={
          <Button variant="primary" type="submit" form={`${id}-range`}>
            完成
          </Button>
        }
      >
        <form
          id={`${id}-range`}
          className="section-stack"
          aria-describedby={rangeError ? `${id}-error` : undefined}
          onSubmit={(event) => {
            event.preventDefault();
            try {
              rangeBody();
              setRangeError('');
              setRangeOpen(false);
            } catch (failure) {
              setRangeError((failure as Error).message);
            }
          }}
        >
          {rangeError && (
            <p id={`${id}-error`} role="alert">
              {rangeError}
            </p>
          )}
          <div className="inline-actions">
            {[
              { label: '最近 1 小时', hours: 1 },
              { label: '最近 24 小时', hours: 24 },
              { label: '最近 7 天', hours: 168 },
            ].map((preset) => (
              <Button
                key={preset.hours}
                onClick={() => {
                  const to = new Date();
                  to.setUTCSeconds(0, 0);
                  setRange({
                    ...range,
                    from: new Date(to.getTime() - preset.hours * 3600000)
                      .toISOString()
                      .slice(0, 16),
                    to: to.toISOString().slice(0, 16),
                  });
                  setRangeError('');
                }}
              >
                {preset.label}
              </Button>
            ))}
          </div>
          <div className="field-grid">
            <Field label="开始时间（UTC）" htmlFor={`${id}-from`}>
              <Input
                id={`${id}-from`}
                type="datetime-local"
                required
                min="1970-01-01T00:00"
                max="2100-12-31T23:59"
                value={range.from}
                onChange={(event) => {
                  setRange({ ...range, from: event.target.value });
                  setRangeError('');
                }}
              />
            </Field>
            <Field label="结束时间（UTC，不含）" htmlFor={`${id}-to`}>
              <Input
                id={`${id}-to`}
                type="datetime-local"
                required
                min="1970-01-01T00:00"
                max="2100-12-31T23:59"
                value={range.to}
                onChange={(event) => {
                  setRange({ ...range, to: event.target.value });
                  setRangeError('');
                }}
              />
            </Field>
          </div>
          <Field label="统计和显示时区" htmlFor={`${id}-zone`}>
            <Select
              id={`${id}-zone`}
              value={range.timezone}
              onChange={(event) => setRange({ ...range, timezone: event.target.value })}
              options={zones.map((zone) => ({ value: zone, label: zone }))}
            />
          </Field>
          <p className="field-help">时间范围左闭右开，输入采用 UTC。调整时间范围不会执行查询。</p>
        </form>
      </Modal>
    </div>
  );
}
