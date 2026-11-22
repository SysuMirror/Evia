import { lazy, Suspense, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import {
  api,
  ApiError,
  type Diagnostics,
  type Environment,
  type Project,
  type Session,
} from '@evia/api-client';
import { Alert, Badge, Button, LoadingPanel, PageHeading, type Tone } from '@evia/design-system';
import { formatInstant } from '../diagnostics/model';
import './styles.css';

const OverviewTrend = lazy(() =>
  import('./overview-trend').then((module) => ({ default: module.OverviewTrend })),
);

type PageProps = { project: Project; session: Session; environment: Environment };
type Attention = { tone: Tone | 'neutral'; title: string; href: string };
type Proof = Diagnostics['pipeline']['proofs'][number];

const environmentLabels: Record<Environment, string> = {
  development: '开发环境',
  staging: '预发环境',
  production: '生产环境',
};

function environmentCount(report: Diagnostics, environment: Environment): number {
  return report.sources.by_environment.find((item) => item.name === environment)?.count ?? 0;
}

function environmentRejections(report: Diagnostics, environment: Environment): number {
  return report.rejections.windows
    .filter((window) => window.environment === environment)
    .flatMap((window) => window.reasons)
    .reduce((sum, reason) => sum + reason.count, 0);
}

function environmentProofs(report: Diagnostics, environment: Environment): Proof[] {
  return report.pipeline.proofs
    .filter((proof) => proof.environment === environment)
    .sort((left, right) => right.observed_at.localeCompare(left.observed_at));
}

function projectPath(projectID: string, route: string, environment: Environment): string {
  return `/projects/${encodeURIComponent(projectID)}/${route}?environment=${environment}`;
}

function attentionItems(
  report: Diagnostics,
  environment: Environment,
  projectID: string,
): Attention[] {
  const path = (route: string) => projectPath(projectID, route, environment);
  const items: Attention[] = [];
  const sourceCount = environmentCount(report, environment);
  const proofs = environmentProofs(report, environment);
  const rejectionCount = environmentRejections(report, environment);
  if (report.sources.status === 'unavailable')
    items.push({ tone: 'error', title: '接入来源清单不可读取', href: path('diagnostics') });
  else if (sourceCount === 0)
    items.push({ tone: 'warning', title: '当前环境没有接入来源', href: path('sources') });
  if (report.pipeline.status === 'unavailable')
    items.push({ tone: 'error', title: '管道探针不可读取', href: path('diagnostics') });
  else if (report.pipeline.status !== 'disabled' && proofs.length === 0)
    items.push({ tone: 'warning', title: '当前环境没有近期管道探针', href: path('diagnostics') });
  if (report.rejections.status === 'unavailable')
    items.push({ tone: 'error', title: '接收拒绝统计不可读取', href: path('diagnostics') });
  else if (rejectionCount > 0)
    items.push({
      tone: 'warning',
      title: `近 ${report.rejections.window_hours} 小时有 ${rejectionCount.toLocaleString('zh-CN')} 个接收拒绝`,
      href: path('diagnostics'),
    });
  return items;
}

function number(value: number): string {
  return value.toLocaleString('zh-CN');
}

export function OverviewPage({ project, environment }: PageProps) {
  const range = useMemo(() => {
    const to = new Date();
    const from = new Date(to.getTime() - 7 * 24 * 60 * 60 * 1000);
    return { from: from.toISOString(), to: to.toISOString() };
  }, []);
  const overview = useQuery({
    queryKey: ['project-overview', project.id, environment, range.from, range.to],
    queryFn: ({ signal }) =>
      api.getProjectOverview(project.id, { environment, ...range, interval: 'day' }, signal),
  });
  const diagnostics = useQuery({
    queryKey: ['diagnostics', project.id],
    queryFn: ({ signal }) => api.getDiagnostics(project.id, signal),
  });
  const route = (name: string) => projectPath(project.id, name, environment);
  const report = diagnostics.data;
  const attention = report ? attentionItems(report, environment, project.id) : [];
  const result = overview.data;

  return (
    <>
      <PageHeading
        title="项目总览"
        summary={`${project.name}，${environmentLabels[environment]}，项目时区 ${project.timezone}`}
        action={
          <Link className="overview-heading-link" to={route('analysis')}>
            打开高级查询
          </Link>
        }
      />

      <section className="overview-facts" aria-labelledby="overview-facts-title">
        <header className="overview-section-heading">
          <div>
            <h2 id="overview-facts-title">近 7 天事件概览</h2>
            <p>全部事件键（包括 sys.*）的去重事实，按 UTC 自然日分桶。</p>
          </div>
          {overview.isFetching && !overview.isPending ? (
            <Badge tone="neutral">正在更新</Badge>
          ) : null}
        </header>

        {overview.isPending ? (
          <LoadingPanel label="正在聚合项目事件概览" />
        ) : overview.isError ? (
          <Alert
            tone="error"
            title="无法读取事件概览"
            action={<Button onClick={() => void overview.refetch()}>重试</Button>}
          >
            {overview.error instanceof ApiError
              ? overview.error.message
              : '概览聚合当前不可用；下方采集诊断仍独立显示。'}
          </Alert>
        ) : result ? (
          <>
            <div className="overview-kpis" aria-label="事件概览指标">
              <article>
                <span>事件总数</span>
                <strong>{number(result.total_events)}</strong>
              </article>
              <article>
                <span>唯一会话</span>
                <strong>{number(result.unique_sessions)}</strong>
                <small>仅非空 session_id</small>
              </article>
              <article>
                <span>已识别 UID</span>
                <strong>{number(result.identified_users)}</strong>
                <small>非空 UID，未做身份解析</small>
              </article>
              <article>
                <span>匿名标识</span>
                <strong>{number(result.anonymous_ids)}</strong>
                <small>仅非空 anonymous_id</small>
              </article>
            </div>

            {result.total_events === 0 ? (
              <div className="overview-empty-facts" role="status">
                <strong>所选范围内没有可查询事件</strong>
                <span>不以零值推断采集正常；请结合下方来源和管道诊断。</span>
              </div>
            ) : (
              <div className="overview-data-grid">
                <section className="overview-trend-panel" aria-labelledby="overview-trend-title">
                  <header>
                    <h3 id="overview-trend-title">事件趋势</h3>
                    <span>单位：次</span>
                  </header>
                  <Suspense fallback={<LoadingPanel label="正在加载趋势图" />}>
                    <OverviewTrend overview={result} timezone={project.timezone} />
                  </Suspense>
                  <details>
                    <summary>查看趋势数据表</summary>
                    <div className="overview-trend-table-wrap">
                      <table>
                        <caption>每日事件与唯一会话</caption>
                        <thead>
                          <tr>
                            <th scope="col">UTC 桶起点</th>
                            <th scope="col">事件</th>
                            <th scope="col">会话</th>
                          </tr>
                        </thead>
                        <tbody>
                          {result.trend.map((point) => (
                            <tr key={point.bucket}>
                              <td>{formatInstant(point.bucket)}</td>
                              <td>{number(point.events)}</td>
                              <td>{number(point.sessions)}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </details>
                </section>
                <section className="overview-top-panel" aria-labelledby="overview-top-title">
                  <header>
                    <h3 id="overview-top-title">热门事件</h3>
                    <span>最多 10 项</span>
                  </header>
                  <ol>
                    {result.top_events.map((item) => (
                      <li key={item.event_key}>
                        <code>{item.event_key}</code>
                        <strong>{number(item.count)}</strong>
                      </li>
                    ))}
                  </ol>
                </section>
              </div>
            )}
            <footer className="overview-coverage">
              <span>精度：精确</span>
              <span>覆盖：暂定</span>
              <span>
                数据水位：
                {result.data_watermark ? formatInstant(result.data_watermark) : '暂无可见事实'}
              </span>
              <span>
                范围：{formatInstant(result.from)} 至 {formatInstant(result.to)}
              </span>
              {result.metadata.known_gaps.length ? <span>已知缺口：管道完整性尚未建立</span> : null}
            </footer>
          </>
        ) : null}
      </section>

      <section className="overview-diagnostics" aria-labelledby="overview-diagnostics-title">
        <header className="overview-section-heading">
          <div>
            <h2 id="overview-diagnostics-title">采集诊断</h2>
            <p>来源、管道与拒绝只作辅助信号，不替代上方事件事实。</p>
          </div>
          <Link to={route('diagnostics')}>查看全部诊断</Link>
        </header>
        {diagnostics.isPending ? (
          <LoadingPanel label="正在读取采集诊断" />
        ) : diagnostics.isError ? (
          <Alert
            tone="error"
            title="采集诊断不可用"
            action={<Button onClick={() => void diagnostics.refetch()}>重试</Button>}
          >
            事件概览与诊断独立加载；当前无法判断来源、探针或接收拒绝状态。
          </Alert>
        ) : report ? (
          <div className="overview-diagnostic-summary">
            <div>
              <span>当前环境来源</span>
              <strong>{number(environmentCount(report, environment))}</strong>
            </div>
            <div>
              <span>近期管道探针</span>
              <strong>{number(environmentProofs(report, environment).length)}</strong>
            </div>
            <div>
              <span>可归属接收拒绝</span>
              <strong>{number(environmentRejections(report, environment))}</strong>
            </div>
            <div className="overview-attention">
              <span>需要关注</span>
              {attention.length ? (
                <ul>
                  {attention.map((item) => (
                    <li key={item.title}>
                      <Badge tone={item.tone}>{item.tone === 'error' ? '不可用' : '需检查'}</Badge>
                      <Link to={item.href}>{item.title}</Link>
                    </li>
                  ))}
                </ul>
              ) : (
                <strong>当前诊断未报告异常</strong>
              )}
            </div>
          </div>
        ) : null}
      </section>
    </>
  );
}
