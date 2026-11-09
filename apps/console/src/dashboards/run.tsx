import { useCallback, useEffect, useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import { api, ApiError, type DashboardExecute, type DashboardVersion } from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  DashboardCard,
  DashboardGrid,
  LoadingPanel,
  Panel,
  Toolbar,
} from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { initialForm, formatTime } from '../analysis-model';
import { CatalogError } from '../catalog-helpers';
import { SavedAnalysisResultPresentation, type SavedRunWork } from '../saved-analysis-run';
import {
  SavedAnalysisTimeControls as SavedTimeControls,
  savedAnalysisExecutionBody as savedExecutionBody,
} from '../saved-analysis-run';
import { DashboardRuntimeFilters } from './runtime-filters';
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
    [filterBaseline, setFilterBaseline] = useState('[]');
  useEffect(() => {
    setFilters([]);
    setFilterBaseline('[]');
    setFilterPending(false);
    setFilterOpen(false);
  }, [version.dashboard_id, version.version]);
  const refs = [
    ...new Map(
      version.definition.widgets.map((w) => [`${w.analysis_id}:${w.analysis_version}`, w]),
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
  const kind = [...analyses.values()].some((a) => a.definition.query.kind === 'funnel')
    ? 'funnel'
    : 'trend';
  const onExecuted = (body: DashboardExecute) => {
    setBaseline(
      JSON.stringify({
        from: body.time_range.from.slice(0, 16),
        to: body.time_range.to.slice(0, 16),
        timezone: body.timezone,
      }),
    );
    setFilterBaseline(JSON.stringify(body.widget_filters ?? []));
  };
  const execution = useDashboardExecution({
    context,
    version,
    analyses,
    disabled: disabled || definitions.some((d) => !d.data) || filterPending,
    body: () => ({
      ...savedExecutionBody(range, kind),
      ...(filters.length ? { widget_filters: filters } : {}),
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
      filterPending ||
      !!attempt ||
      active;
  useEffect(
    () => onWorkChange({ dirty, busy, retryPending: !!attempt }),
    [dirty, busy, attempt, onWorkChange],
  );
  useEffect(() => () => onWorkChange({ dirty: false, busy: false }), [onWorkChange]);
  useEffect(() => {
    for (const item of definitions)
      if (item.error instanceof ApiError && [401, 403].includes(item.error.status))
        onDenied(item.error);
  }, [definitions.map((d) => d.error).join(), onDenied]);
  const frozen = disabled || busy || !!attempt;
  const changed =
    !!snapshot &&
    (snapshot.version.version !== version.version ||
      JSON.stringify(range) !== baseline ||
      JSON.stringify(filters) !== filterBaseline);
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
                disabled={frozen || active || definitions.some((d) => !d.data)}
                onClick={() => setFilterOpen(true)}
              >
                组件运行筛选 ({filters.length})
              </Button>
              <Button
                variant="primary"
                loading={busy && !active}
                disabled={
                  disabled || busy || active || filterPending || definitions.some((d) => !d.data)
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
          <CatalogError key={i} error={d.error} retry={() => void d.refetch()} />
        ))}
        {definitions.some((d) => d.isPending) && <LoadingPanel label="正在读取固定分析版本" />}
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
      <DashboardRuntimeFilters
        key={`${version.dashboard_id}:${version.version}`}
        context={context}
        version={version}
        analyses={analyses}
        open={filterOpen}
        disabled={frozen}
        onClose={() => setFilterOpen(false)}
        onApply={setFilters}
        onPendingChange={setFilterPending}
        onDenied={onDenied}
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
            analysis = snapshot?.analyses.get(`${w.analysis_id}:${w.analysis_version}`),
            result = snapshot?.results[w.widget_id],
            resultError = snapshot?.resultErrors[w.widget_id];
          return {
            id: w.widget_id,
            reading_order: w.reading_order,
            layout: w.layout,
            content: (
              <DashboardCard
                title={w.title}
                summary={`分析 v${w.analysis_version}`}
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
                {run && analysis && snapshot ? (
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
