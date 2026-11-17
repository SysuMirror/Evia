import { useEffect, useId, useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type ComparisonSpec,
  type DashboardExecute,
  type DashboardVersion,
  type DashboardWidget,
  type ParameterValues,
  type QueryJob,
  type QueryResult,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  DashboardCard,
  DashboardGrid,
  DataSurface,
  DataTable,
  EmptyPanel,
  LoadingPanel,
  Panel,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { initialForm, formatTime } from '../analysis-model';
import { CatalogError } from '../catalog-helpers';
import { SavedAnalysisResultPresentation, type SavedRunWork } from '../saved-analysis-run';
import {
  SavedAnalysisTimeControls as SavedTimeControls,
  savedAnalysisExecutionBody as savedExecutionBody,
} from '../saved-analysis-run';
import { displayCell } from '../saved-analysis-run/result-table';
import { DashboardRuntimeFilters } from './runtime-filters';
import { ComparisonControls } from './comparison-controls';
import { DashboardParameterValues } from './parameter-run-values';
import { widgetSourceLabel } from './model';
import { activeDashboardRun, useDashboardExecution } from './use-execution';
export type { SavedRunWork } from '../saved-analysis-run';
const labels = {
  queued: '排队中',
  running: '执行中',
  succeeded: '已完成',
  partial: '部分完成',
  failed: '执行失败',
  canceled: '已取消',
  expired: '已过期',
};
export function DashboardRunView({
  context,
  version,
  disabled,
  onWorkChange,
  onDenied,
}: {
  context: CatalogPageProps;
  version: DashboardVersion;
  disabled: boolean;
  onWorkChange: (work: SavedRunWork) => void;
  onDenied: (error: Error) => void;
}) {
  const [range, setRange] = useState(() => {
      const f = initialForm(context.project.timezone);
      return { from: f.from, to: f.to, timezone: context.project.timezone };
    }),
    [baseline, setBaseline] = useState(() => JSON.stringify(range)),
    [rangeOpen, setRangeOpen] = useState(false),
    [rangeError, setRangeError] = useState(''),
    [filters, setFilters] = useState<NonNullable<DashboardExecute['widget_filters']>>([]),
    [filterOpen, setFilterOpen] = useState(false),
    [filterPending, setFilterPending] = useState(false),
    [filterBaseline, setFilterBaseline] = useState('[]'),
    [comparisonSpec, setComparisonSpec] = useState<ComparisonSpec | null>(null),
    [parameterValues, setParameterValues] = useState<ParameterValues>({}),
    [parameterOpen, setParameterOpen] = useState(false),
    [parameterPending, setParameterPending] = useState(false),
    [parameterBaseline, setParameterBaseline] = useState('{}');
  const controlID = useId();
  const declaredParameters = version.definition.parameters ?? [];
  useEffect(() => {
    setFilters([]);
    setFilterBaseline('[]');
    setFilterPending(false);
    setFilterOpen(false);
    setParameterValues({});
    setParameterBaseline('{}');
    setParameterPending(false);
    setParameterOpen(false);
  }, [version.dashboard_id, version.version]);
  // Only analysis-sourced widgets need a saved-analysis version prefetch.
  // Metric-sourced widgets carry metric_ref and are rendered without it.
  const refs = [
    ...new Map(
      version.definition.widgets
        .filter(
          (w): w is typeof w & { analysis_id: string; analysis_version: number } =>
            w.analysis_id !== undefined && w.analysis_version !== undefined,
        )
        .map((w) => [`${w.analysis_id}:${w.analysis_version}`, w]),
    ).values(),
  ];
  const definitions = useQueries({
    queries: refs.map((w) => ({
      queryKey: [
        'dashboards',
        context.project.id,
        context.environment,
        context.session.user_id,
        'analysis-version',
        w.analysis_id,
        w.analysis_version,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.getSavedAnalysisVersion(context.project.id, w.analysis_id, w.analysis_version, signal),
    })),
  });
  const analyses = new Map(
    definitions.flatMap((result) =>
      result.data
        ? [[`${result.data.analysis_id}:${result.data.version}`, result.data] as const]
        : [],
    ),
  );
  // Metric-sourced widgets pin immutable metric versions; their pinned event
  // catalog is needed by the runtime filter modal and by the execution gate.
  const metricRefs = [
    ...new Map(
      version.definition.widgets
        .filter(
          (w): w is typeof w & { metric_ref: NonNullable<DashboardWidget['metric_ref']> } =>
            !!w.metric_ref,
        )
        .map((w) => [`${w.metric_ref.metric_id}:${w.metric_ref.version}`, w]),
    ).values(),
  ];
  const metricDefinitionQueries = useQueries({
    queries: metricRefs.map((w) => ({
      queryKey: [
        'dashboards',
        context.project.id,
        context.environment,
        context.session.user_id,
        'metric-version',
        w.metric_ref.metric_id,
        w.metric_ref.version,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.getMetricVersion(
          context.project.id,
          w.metric_ref.metric_id,
          w.metric_ref.version,
          signal,
        ),
    })),
  });
  const metrics = new Map(
    metricDefinitionQueries.flatMap((result) =>
      result.data
        ? [[`${result.data.metric_id}:${result.data.version}`, result.data] as const]
        : [],
    ),
  );
  const kind = [...analyses.values()].some((a) => a.definition.query.kind === 'funnel')
    ? 'funnel'
    : 'trend';
  const sourcesReady =
    definitions.every((d) => d.data) && metricDefinitionQueries.every((d) => d.data);
  const onExecuted = (body: DashboardExecute) => {
    setBaseline(
      JSON.stringify({
        from: body.time_range.from.slice(0, 16),
        to: body.time_range.to.slice(0, 16),
        timezone: body.timezone,
      }),
    );
    setFilterBaseline(JSON.stringify(body.widget_filters ?? []));
    setParameterBaseline(JSON.stringify(body.parameter_values ?? {}));
  };
  const execution = useDashboardExecution({
    context,
    version,
    analyses,
    disabled: disabled || !sourcesReady || filterPending || parameterPending,
    body: () => ({
      ...savedExecutionBody(range, kind),
      ...(filters.length ? { widget_filters: filters } : {}),
      ...(comparisonSpec ? { comparison: comparisonSpec } : {}),
      ...(Object.keys(parameterValues).length ? { parameter_values: parameterValues } : {}),
    }),
    onInvalid: (message) => {
      setRangeError(message);
      setRangeOpen(true);
    },
    onExecuted,
    onDenied,
  });
  const { snapshot, attempt, busy, observing, error, notice } = execution;
  const active = activeDashboardRun(snapshot?.run),
    dirty =
      JSON.stringify(range) !== baseline ||
      JSON.stringify(filters) !== filterBaseline ||
      JSON.stringify(parameterValues) !== parameterBaseline ||
      filterPending ||
      parameterPending ||
      !!attempt ||
      active;
  useEffect(
    () => onWorkChange({ dirty, busy, retryPending: !!attempt }),
    [dirty, busy, attempt, onWorkChange],
  );
  useEffect(() => () => onWorkChange({ dirty: false, busy: false }), [onWorkChange]);
  useEffect(() => {
    for (const item of [...definitions, ...metricDefinitionQueries])
      if (item.error instanceof ApiError && [401, 403].includes(item.error.status))
        onDenied(item.error);
  }, [
    definitions.map((d) => d.error).join(),
    metricDefinitionQueries.map((d) => d.error).join(),
    onDenied,
  ]);
  const frozen = disabled || busy || !!attempt;
  const changed =
    !!snapshot &&
    (snapshot.version.version !== version.version ||
      JSON.stringify(range) !== baseline ||
      JSON.stringify(filters) !== filterBaseline ||
      JSON.stringify(parameterValues) !== parameterBaseline);
  return (
    <div className="section-stack dashboard-run">
      <Panel label="看板执行条件" className="dashboard-run-controls">
        <Toolbar
          actions={
            <>
              <Button disabled={frozen || active} onClick={() => setRangeOpen(true)}>
                统一时间范围
              </Button>
              <Button
                disabled={frozen || active || !sourcesReady}
                onClick={() => setFilterOpen(true)}
              >
                组件运行筛选 ({filters.length})
              </Button>
              {declaredParameters.length > 0 && (
                <Button
                  disabled={frozen || active || !sourcesReady}
                  onClick={() => setParameterOpen(true)}
                >
                  运行参数 ({Object.keys(parameterValues).length}/{declaredParameters.length})
                </Button>
              )}
              <Button
                variant="primary"
                loading={busy && !active}
                disabled={
                  disabled || busy || active || filterPending || parameterPending || !sourcesReady
                }
                onClick={() => void execution.execute()}
              >
                {attempt ? '重试运行看板' : '运行看板'}
              </Button>
              {active && (
                <Button disabled={busy} onClick={() => void execution.cancel()}>
                  取消全部查询
                </Button>
              )}
            </>
          }
        >
          <span>
            v{version.version}，{version.definition.widgets.length} 个组件
          </span>
          <span className="field-help">
            {formatTime(`${range.from}Z`, range.timezone)} 至{' '}
            {formatTime(`${range.to}Z`, range.timezone)}，{range.timezone}
          </span>
        </Toolbar>
        <CatalogError error={error} />
        {definitions.map((d, i) => (
          <CatalogError key={`analysis-${i}`} error={d.error} retry={() => void d.refetch()} />
        ))}
        {metricDefinitionQueries.map((d, i) => (
          <CatalogError key={`metric-${i}`} error={d.error} retry={() => void d.refetch()} />
        ))}
        {(definitions.some((d) => d.isPending) ||
          metricDefinitionQueries.some((d) => d.isPending)) && (
          <LoadingPanel label="正在读取固定分析与指标版本" />
        )}
        {attempt && !busy && (
          <Alert tone="warning" title="运行响应尚未确认">
            已保留原始版本、时间、筛选与请求标识，重试不会重复入队。
          </Alert>
        )}
        {snapshot && (
          <div className="dashboard-run-status">
            <Badge
              tone={
                snapshot.run.state === 'succeeded'
                  ? 'success'
                  : snapshot.run.state === 'failed'
                    ? 'error'
                    : 'neutral'
              }
            >
              {labels[snapshot.run.state]}
            </Badge>
            <span>本次运行 v{snapshot.run.version}</span>
            <span>{notice || (observing ? '正在检查组件状态' : '')}</span>
            {!observing && (
              <Button size="small" disabled={busy} onClick={execution.resume}>
                继续检查状态
              </Button>
            )}
          </div>
        )}
      </Panel>
      <SavedTimeControls
        range={range}
        setRange={setRange}
        rangeOpen={rangeOpen}
        rangeError={rangeError}
        setRangeError={setRangeError}
        kind={kind}
        onClose={() => setRangeOpen(false)}
      />
      <ComparisonControls
        id={controlID}
        value={comparisonSpec}
        disabled={frozen}
        onChange={setComparisonSpec}
      />
      <DashboardRuntimeFilters
        key={`${version.dashboard_id}:${version.version}`}
        context={context}
        version={version}
        analyses={analyses}
        metrics={metrics}
        open={filterOpen}
        disabled={frozen}
        onClose={() => setFilterOpen(false)}
        onApply={setFilters}
        onPendingChange={setFilterPending}
        onDenied={onDenied}
      />
      <DashboardParameterValues
        key={`${version.dashboard_id}:${version.version}:parameters`}
        declarations={declaredParameters}
        initial={parameterValues}
        open={parameterOpen}
        disabled={frozen}
        onClose={() => setParameterOpen(false)}
        onApply={setParameterValues}
        onPendingChange={setParameterPending}
      />
      {changed && (
        <Alert tone="warning" title="执行条件已修改">
          下方保留上次运行的固定版本与结果，运行后更新。
        </Alert>
      )}
      <p className="field-help">各组件独立执行并报告精度与覆盖，同次运行不代表原子数据快照。</p>
      <DashboardGrid
        label="看板查询结果"
        items={(snapshot?.version.definition.widgets ?? version.definition.widgets).map((w) => {
          const run = snapshot?.run.widgets.find((r) => r.widget_id === w.widget_id),
            result = snapshot?.results[w.widget_id],
            resultError = snapshot?.resultErrors[w.widget_id],
            comparisonResult = snapshot?.comparisonResults[`${w.widget_id}:comparison`],
            comparisonResultError = snapshot?.comparisonResultErrors[`${w.widget_id}:comparison`],
            isMetric = !!w.metric_ref,
            analysis = isMetric
              ? undefined
              : snapshot?.analyses.get(`${w.analysis_id}:${w.analysis_version}`);
          return {
            id: w.widget_id,
            reading_order: w.reading_order,
            layout: w.layout,
            content: (
              <DashboardCard
                title={w.title}
                summary={widgetSourceLabel(w)}
                action={
                  run && ['queued', 'running'].includes(run.query.state) ? (
                    <Button
                      size="small"
                      disabled={busy}
                      onClick={() => void execution.cancel(w.widget_id)}
                      aria-label={`取消组件 ${w.title}`}
                    >
                      取消组件查询
                    </Button>
                  ) : undefined
                }
              >
                {snapshot && run && isMetric ? (
                  <>
                    <MetricWidgetPresentation
                      body={snapshot.body}
                      job={run.query}
                      result={result}
                      observing={
                        observing &&
                        (['queued', 'running'].includes(run.query.state) ||
                          (run.query.state === 'succeeded' && !result))
                      }
                      onResume={execution.resume}
                    />
                    <CatalogError error={resultError ?? null} retry={execution.resume} />
                  </>
                ) : snapshot && run && analysis ? (
                  <>
                    <SavedAnalysisResultPresentation
                      compact
                      snapshot={{
                        key: snapshot.key,
                        version: analysis,
                        body: snapshot.body,
                        job: run.query,
                        result,
                      }}
                      changed={false}
                      observing={
                        observing &&
                        (['queued', 'running'].includes(run.query.state) ||
                          (run.query.state === 'succeeded' && !result))
                      }
                      notice=""
                      onResume={execution.resume}
                    />
                    <CatalogError error={resultError ?? null} retry={execution.resume} />
                  </>
                ) : (
                  <div className="dashboard-widget-preview">
                    <p>尚未运行，选择统一时间范围后运行看板。</p>
                  </div>
                )}
                {snapshot && run?.comparison && (
                  <div className="dashboard-comparison-pane">
                    <Badge tone="neutral">比较期窗口</Badge>
                    {isMetric ? (
                      <>
                        <MetricWidgetPresentation
                          body={snapshot.body}
                          job={run.comparison}
                          result={comparisonResult}
                          observing={
                            observing &&
                            (['queued', 'running'].includes(run.comparison.state) ||
                              (run.comparison.state === 'succeeded' && !comparisonResult))
                          }
                          onResume={execution.resume}
                        />
                        <CatalogError
                          error={comparisonResultError ?? null}
                          retry={execution.resume}
                        />
                      </>
                    ) : analysis ? (
                      <>
                        <SavedAnalysisResultPresentation
                          compact
                          snapshot={{
                            key: `${snapshot.key}:comparison`,
                            version: analysis,
                            body: snapshot.body,
                            job: run.comparison,
                            result: comparisonResult,
                          }}
                          changed={false}
                          observing={
                            observing &&
                            (['queued', 'running'].includes(run.comparison.state) ||
                              (run.comparison.state === 'succeeded' && !comparisonResult))
                          }
                          notice=""
                          onResume={execution.resume}
                        />
                        <CatalogError
                          error={comparisonResultError ?? null}
                          retry={execution.resume}
                        />
                      </>
                    ) : null}
                  </div>
                )}
              </DashboardCard>
            ),
          };
        })}
      />
      {snapshot && (
        <details>
          <summary>本次运行的版本与条件</summary>
          <CodeBlock>
            {JSON.stringify(
              {
                run_id: snapshot.run.run_id,
                version: snapshot.version.version,
                time_range: snapshot.run.time_range,
                timezone: snapshot.run.timezone,
                widget_filters: snapshot.run.widget_filters ?? [],
              },
              null,
              2,
            )}
          </CodeBlock>
        </details>
      )}
    </div>
  );
}

/**
 * Generic result presentation for metric-sourced widgets. Unlike saved-analysis
 * widgets there is no pinned analysis definition to derive kind-specific column
 * names, so columns come straight from the QueryResult contract and cells reuse
 * the shared type-aware displayCell renderer.
 */
function MetricWidgetPresentation({
  body,
  job,
  result,
  observing,
  onResume,
}: {
  body: DashboardExecute;
  job: QueryJob;
  result?: QueryResult;
  observing: boolean;
  onResume: () => void;
}) {
  const rows = result?.rows ?? [];
  const columns: DataTableColumn<Record<string, unknown>>[] = (result?.columns ?? []).map(
    (column) => ({
      key: column.key,
      header: column.label || column.key,
      render: (row) => displayCell(row[column.key], column.type, body.timezone),
    }),
  );
  return (
    <div className="section-stack">
      <div className="inline-actions">
        <Badge
          tone={
            job.state === 'succeeded'
              ? 'success'
              : job.state === 'failed' || job.state === 'expired'
                ? 'error'
                : 'neutral'
          }
        >
          {labels[job.state]}
        </Badge>
        {result && (
          <>
            <Badge tone={result.metadata.accuracy === 'approximate' ? 'warning' : 'neutral'}>
              {result.metadata.accuracy === 'exact'
                ? '精确'
                : result.metadata.accuracy === 'approximate'
                  ? '近似'
                  : '不适用'}
            </Badge>
            <Badge tone={result.metadata.coverage.state === 'settled' ? 'success' : 'warning'}>
              {result.metadata.coverage.state === 'settled' ? '覆盖已稳定' : '覆盖暂定'}
            </Badge>
          </>
        )}
      </div>
      <div className="analysis-query-context">
        {formatTime(body.time_range.from, body.timezone)} 至{' '}
        {formatTime(body.time_range.to, body.timezone)}（不含），{body.timezone}
      </div>
      {!observing &&
        (['queued', 'running'].includes(job.state) || (job.state === 'succeeded' && !result)) && (
          <Button onClick={onResume}>继续检查状态</Button>
        )}
      {job.state === 'failed' && (
        <Alert tone="error" title="查询执行失败">
          {job.error?.code}，
          {job.error?.retryable ? '可保留条件重新运行。' : '请检查指标配置与时间范围。'}
          {job.error?.field_paths?.join('，')}
        </Alert>
      )}
      {job.state === 'canceled' && <EmptyPanel description="查询已取消，可重新运行。" />}
      {job.state === 'expired' && (
        <EmptyPanel description="查询已过期，结果无法恢复，请重新运行。" />
      )}
      {result && (
        <>
          <details>
            <summary>完整结果表（{rows.length} 行）</summary>
            <DataSurface label="指标组件结果表">
              <DataTable
                caption="固定指标版本的完整有界结果"
                rows={rows}
                columns={columns}
                rowKey={(row) => JSON.stringify(row)}
                empty="该范围内无匹配结果，不等同于数值零。"
              />
            </DataSurface>
          </details>
          <p className="field-help">返回 {rows.length} 行，按固定指标口径与统一时间范围生成。</p>
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
                  {formatTime(result.metadata.coverage.time_range.from, body.timezone)} 至{' '}
                  {formatTime(result.metadata.coverage.time_range.to, body.timezone)}（不含）
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
                    ? formatTime(result.metadata.data_watermark, body.timezone)
                    : '无观测数据'}
                  ，不代表数据完整到达
                </dd>
              </div>
              <div>
                <dt>生成时间</dt>
                <dd>
                  {formatTime(result.metadata.generated_at, body.timezone)}，
                  {result.metadata.cache_hit ? '命中缓存' : '未命中缓存'}
                </dd>
              </div>
            </dl>
          </details>
          <details>
            <summary>原始结果 JSON</summary>
            <CodeBlock label="完整结果 JSON">{JSON.stringify(result, null, 2)}</CodeBlock>
          </details>
        </>
      )}
    </div>
  );
}
