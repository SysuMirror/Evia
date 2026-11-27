import type { QueryJob, SavedAnalysisTemplate } from '@evia/api-client';
import { Component, lazy, Suspense, useMemo, type ReactNode } from 'react';
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  CopyButton,
  DataSurface,
  DataTable,
  EmptyPanel,
  LoadingPanel,
  Panel,
  ResultHeader,
} from '@evia/design-system';
import { PathFlowVisual, ProportionBarsVisual, RetentionCohortMatrix } from '../analysis-visuals';
import { formatTime } from '../analysis-model';
import { environmentLabel } from '../catalog-helpers';
import { savedAnalysisResultChart } from './result-chart';
import { metricInfo } from './result-labels';
import { displayCell, savedResultColumns } from './result-table';
import { activeSavedJob as active, type ExecutionSnapshot } from './types';

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
const jobLabels: Record<QueryJob['state'], string> = {
  queued: '排队中',
  running: '执行中',
  succeeded: '已完成',
  failed: '执行失败',
  canceled: '已取消',
  expired: '已过期',
};

/** Kind-specific result semantics. The row-count line stays generic; only the
 * explanatory note changes so non-trend kinds no longer show trend-only wording. */
function savedResultHint(kind: SavedAnalysisTemplate['kind']): string {
  switch (kind) {
    case 'trend':
      return '缺失时间桶未补零，各桶去重数不能相加为整段去重数。';
    case 'funnel':
      return '转化率按相邻步骤计算（较上一步转化率），步骤按固定顺序排列，未到达的步骤不补零。';
    case 'retention':
      return '0 期留存率恒为 1.0；各队列按起始事件所在周期划分，期数受配置上限约束。';
    case 'segment':
      return '每个主体按最早分群事件的标量值唯一归组，缺失值归入固定缺失组。';
    case 'path':
      return '路径按事件因果序排列并受深度上限约束，事件不足深度的主体不参与统计。';
    case 'attribution':
      return '每个转化主体只归因一次；窗口内无触点的转化归入直接来源（__direct__），占比按转化主体数计算。';
    default:
      return '该范围内无匹配结果不等同于数值零。';
  }
}

export function SavedResultPresentation({
  snapshot,
  compact = false,
  changed,
  observing,
  notice,
  onResume,
}: {
  snapshot: ExecutionSnapshot;
  compact?: boolean;
  changed: boolean;
  observing: boolean;
  notice: string;
  onResume: () => void;
}) {
  const result = snapshot.result,
    rows = result?.rows ?? [];
  const queryKind = snapshot.version.definition.query.kind;
  const columns = result ? savedResultColumns(snapshot, result) : [];
  const metricName = (key: string) =>
    metricInfo(snapshot.version.definition, Number(key.slice(7))).label;
  const chart = useMemo(
    () => (result ? savedAnalysisResultChart(snapshot.version, result, snapshot.body) : undefined),
    [snapshot.version, result, snapshot.body],
  );
  return (
    <Panel label="已发布分析结果" className="saved-analysis-result">
      {!compact && (
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
                  <Badge tone={result.metadata.accuracy === 'approximate' ? 'warning' : 'neutral'}>
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
      )}
      <div className="section-stack">
        {compact && (
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
                <Badge tone={result.metadata.accuracy === 'approximate' ? 'warning' : 'neutral'}>
                  {result.metadata.accuracy === 'exact'
                    ? '精确'
                    : result.metadata.accuracy === 'approximate'
                      ? '近似'
                      : '明细'}
                </Badge>
                <Badge tone={result.metadata.coverage.state === 'settled' ? 'success' : 'warning'}>
                  {result.metadata.coverage.state === 'settled' ? '覆盖已稳定' : '覆盖暂定'}
                </Badge>
              </>
            )}
          </div>
        )}
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
            <Button onClick={onResume}>继续检查状态</Button>
          )}
        {snapshot.job.state === 'failed' && (
          <Alert tone="error" title="查询执行失败">
            {snapshot.job.error?.code}，
            {snapshot.job.error?.retryable ? '可保留条件重新运行。' : '请检查配置与时间范围。'}
            {snapshot.job.error?.field_paths?.join('，')}
          </Alert>
        )}
        {snapshot.job.state === 'canceled' && <EmptyPanel description="查询已取消，可重新运行。" />}
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
            {queryKind === 'retention' && rows.length > 0 && (
              <RetentionCohortMatrix
                rows={rows}
                granularity={snapshot.version.definition.query.retention?.granularity}
                timezone={snapshot.body.timezone}
              />
            )}
            {queryKind === 'segment' && rows.length > 0 && (
              <ProportionBarsVisual
                title="主体分群分布"
                subtitle="按分群主体占比降序排列"
                items={rows.map((row, idx) => {
                  const raw = row.segment_value;
                  const missing = raw === null || raw === undefined || raw === '';
                  return {
                    key: `${idx}-${String(raw)}`,
                    label: missing ? '缺失值' : String(raw),
                    badge: missing ? '缺失组' : undefined,
                    value: typeof row.subjects === 'number' ? row.subjects : 0,
                    share: typeof row.share === 'number' ? row.share : 0,
                  };
                })}
              />
            )}
            {queryKind === 'attribution' && rows.length > 0 && (
              <ProportionBarsVisual
                title="触点归因贡献分布"
                subtitle="各触点转化归因占比"
                accentColor="#0d9488"
                items={rows.map((row, idx) => {
                  const key = String(row.touchpoint ?? row.event_key ?? `触点 ${idx + 1}`);
                  const isDirect = key === '__direct__';
                  const credit =
                    typeof row.credit === 'number' ? `权重 ${row.credit.toFixed(2)}` : undefined;
                  return {
                    key: `${idx}-${key}`,
                    label: isDirect ? '直接来源 (__direct__)' : key,
                    badge: isDirect ? '无触点' : undefined,
                    value: typeof row.subjects === 'number' ? row.subjects : 0,
                    share: typeof row.share === 'number' ? row.share : 0,
                    extra: credit,
                  };
                })}
              />
            )}
            {queryKind === 'path' && rows.length > 0 && <PathFlowVisual rows={rows} />}
            {compact ? (
              <details>
                <summary>完整结果表（{rows.length} 行）</summary>
                <DataSurface label="保存分析结果表">
                  <DataTable
                    caption="已发布分析的完整有界结果"
                    rows={rows}
                    columns={columns}
                    rowKey={(row) => String(rows.indexOf(row))}
                    empty="该范围内无匹配结果，不等同于数值零。"
                  />
                </DataSurface>
              </details>
            ) : (
              <>
                <DataSurface label="保存分析结果表">
                  <DataTable
                    caption="已发布分析的完整有界结果"
                    rows={rows}
                    columns={columns}
                    rowKey={(row) => String(rows.indexOf(row))}
                    empty="该范围内无匹配结果，不等同于数值零。"
                  />
                </DataSurface>
              </>
            )}
            <p className="field-help">
              返回 {rows.length} 行，上限 {snapshot.version.definition.query.limit ?? 200} 行。
              {savedResultHint(queryKind)}
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
                    {formatTime(result.metadata.coverage.time_range.from, snapshot.body.timezone)}{' '}
                    至 {formatTime(result.metadata.coverage.time_range.to, snapshot.body.timezone)}
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
  );
}
