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
import {
  Alert,
  Badge,
  Button,
  DataTable,
  LoadingPanel,
  PageHeading,
  type DataTableColumn,
  type Tone,
} from '@evia/design-system';
import {
  formatAge,
  formatInstant,
  sectionStatusLabel,
  sectionStatusTone,
  shortUUID,
} from '../diagnostics/model';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };
type Attention = { tone: Tone | 'neutral'; title: string; detail: string; href: string };
type Signal = { label: string; status: Diagnostics['pipeline']['status']; detail: string };
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

function overviewState(report: Diagnostics, environment: Environment) {
  const sourceCount = environmentCount(report, environment);
  const proofs = environmentProofs(report, environment);
  const rejectionCount = environmentRejections(report, environment);
  const policy = report.budget.environments.find((item) => item.environment === environment);
  const pipelineStatus =
    report.pipeline.status === 'ok' && proofs.length === 0 ? 'no_data' : report.pipeline.status;
  const unavailable = [
    report.sources.status,
    report.budget.status,
    report.pipeline.status,
    report.rejections.status,
  ].some((status) => status === 'unavailable');
  const needsAttention =
    unavailable ||
    !report.features.collection_enabled ||
    sourceCount === 0 ||
    pipelineStatus !== 'ok' ||
    rejectionCount > 0;
  const headline = unavailable
    ? '部分运行信号当前不可读取'
    : !report.features.collection_enabled
      ? '当前未启用实时采集'
      : sourceCount === 0
        ? '当前环境还没有接入来源'
        : pipelineStatus !== 'ok'
          ? '当前环境尚无可用管道探针'
          : rejectionCount > 0
            ? '采集有可用证据，同时存在接收拒绝'
            : '当前环境有可用采集证据';
  const tone: Tone = unavailable ? 'error' : needsAttention ? 'warning' : 'success';

  const signals: Signal[] = [
    {
      label: '接入来源',
      status:
        report.sources.status === 'ok' && sourceCount === 0 ? 'no_data' : report.sources.status,
      detail: `${environmentLabels[environment]} ${sourceCount.toLocaleString('zh-CN')} 个来源`,
    },
    {
      label: '采集预算',
      status: report.budget.status,
      detail: policy
        ? `${policy.rate_per_second.toLocaleString('zh-CN')} 事件/秒，突发 ${policy.burst.toLocaleString('zh-CN')}`
        : report.budget.status === 'disabled'
          ? '能力未启用'
          : '当前环境没有策略明细',
    },
    {
      label: '管道探针',
      status: pipelineStatus,
      detail: proofs[0]
        ? `${formatAge(proofs[0].age_seconds)}前观察到，不能代表事实完整性`
        : '保留窗口内没有当前环境的探针收据',
    },
    {
      label: '接收拒绝',
      status: report.rejections.status,
      detail:
        report.rejections.status === 'ok'
          ? `近 ${report.rejections.window_hours} 小时 ${rejectionCount.toLocaleString('zh-CN')} 个可归属拒绝`
          : report.rejections.status === 'disabled'
            ? '能力未启用'
            : '拒绝统计当前不可读取',
    },
  ];

  return { sourceCount, proofs, rejectionCount, headline, tone, signals };
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

  if (!report.features.collection_enabled)
    items.push({
      tone: 'neutral',
      title: '实时采集未启用',
      detail: '当前服务进程不会提供采集预算或接收统计。',
      href: path('diagnostics'),
    });
  if (report.sources.status === 'unavailable')
    items.push({
      tone: 'error',
      title: '接入来源清单不可读取',
      detail: '诊断请求未能读取项目来源配置。',
      href: path('diagnostics'),
    });
  else if (sourceCount === 0)
    items.push({
      tone: 'warning',
      title: '当前环境没有接入来源',
      detail: '先创建并启用来源，才会有可归属的采集流量。',
      href: path('sources'),
    });
  if (report.budget.status === 'unavailable')
    items.push({
      tone: 'error',
      title: '采集预算状态不可读取',
      detail: report.budget.error_code ?? 'Redis 预算依赖当前不可用。',
      href: path('diagnostics'),
    });
  if (report.pipeline.status === 'unavailable')
    items.push({
      tone: 'error',
      title: '管道探针不可读取',
      detail: report.pipeline.error_code ?? 'ClickHouse 探针依赖当前不可用。',
      href: path('diagnostics'),
    });
  else if (report.pipeline.status !== 'disabled' && proofs.length === 0)
    items.push({
      tone: 'warning',
      title: '当前环境没有近期管道探针',
      detail: '保留窗口内没有发布就绪收据；这不是事件事实完整性结论。',
      href: path('diagnostics'),
    });
  if (report.rejections.status === 'unavailable')
    items.push({
      tone: 'error',
      title: '接收拒绝统计不可读取',
      detail: report.rejections.error_code ?? '近 24 小时拒绝计数当前不可用。',
      href: path('diagnostics'),
    });
  else if (rejectionCount > 0)
    items.push({
      tone: 'warning',
      title: `近 ${report.rejections.window_hours} 小时有 ${rejectionCount.toLocaleString('zh-CN')} 个接收拒绝`,
      detail: '进入采集诊断查看按小时和原因拆分的真实计数。',
      href: path('diagnostics'),
    });
  return items;
}

export function OverviewPage({ project, environment }: PageProps) {
  const diagnostics = useQuery({
    queryKey: ['diagnostics', project.id],
    queryFn: ({ signal }) => api.getDiagnostics(project.id, signal),
  });
  const route = (name: string) => projectPath(project.id, name, environment);
  const report = diagnostics.data;
  const state = report ? overviewState(report, environment) : undefined;
  const attention = report ? attentionItems(report, environment, project.id) : [];

  const proofColumns: DataTableColumn<Proof>[] = [
    { key: 'observed', header: '观察时间', render: (row) => formatInstant(row.observed_at) },
    { key: 'age', header: '距今', render: (row) => formatAge(row.age_seconds) },
    { key: 'release', header: '发布', render: (row) => shortUUID(row.release_id) },
    { key: 'revision', header: '修订', render: (row) => String(row.revision) },
    { key: 'count', header: '累计探针', render: (row) => String(row.proof_count) },
  ];

  return (
    <>
      <PageHeading
        title="项目总览"
        summary={`${project.name}，${environmentLabels[environment]}，项目时区 ${project.timezone}`}
        action={
          <Link className="overview-heading-link" to={route('heatmap')}>
            查看视口点击分布
          </Link>
        }
      />

      {diagnostics.isPending ? (
        <section className="overview-anchor" aria-label="项目运行概况">
          <LoadingPanel label="正在读取项目运行信号" />
        </section>
      ) : diagnostics.isError ? (
        <section className="overview-anchor" aria-label="项目运行概况">
          <Alert
            tone="error"
            title="无法读取项目运行信号"
            action={<Button onClick={() => void diagnostics.refetch()}>重试</Button>}
          >
            {diagnostics.error instanceof ApiError
              ? diagnostics.error.message
              : '诊断数据当前不可用。项目身份与调查入口仍可使用。'}
          </Alert>
          <p className="overview-coverage-note">
            未读取到诊断 API 时，不推断来源、数据新鲜度或覆盖状态。
          </p>
        </section>
      ) : report && state ? (
        <section
          className={`overview-anchor overview-anchor-${state.tone}`}
          aria-label="项目运行概况"
        >
          <div className="overview-anchor-copy">
            <Badge tone={state.tone}>{state.headline}</Badge>
            <h2>{environmentLabels[environment]}运行概况</h2>
            <p>
              诊断生成于 {formatInstant(report.generated_at)}
              。管道探针只证明发布收据存在；事件级完整性与查询覆盖须在具体结果中判断。
            </p>
          </div>
          <ol className="overview-signal-path" aria-label="采集链路信号">
            {state.signals.map((signal) => (
              <li key={signal.label} data-status={signal.status}>
                <span className="overview-signal-marker" aria-hidden="true" />
                <div>
                  <strong>{signal.label}</strong>
                  <Badge tone={sectionStatusTone(signal.status)}>
                    {sectionStatusLabel(signal.status)}
                  </Badge>
                  <p>{signal.detail}</p>
                </div>
              </li>
            ))}
          </ol>
        </section>
      ) : null}

      <div className="overview-support-grid">
        <section className="overview-panel" aria-labelledby="overview-attention-title">
          <header>
            <div>
              <h2 id="overview-attention-title">需要关注</h2>
              <p>仅列出诊断 API 能确认的异常、缺失或拒绝。</p>
            </div>
            {report && (
              <Badge tone={attention.length ? 'warning' : 'success'}>{attention.length} 项</Badge>
            )}
          </header>
          {diagnostics.isPending ? (
            <LoadingPanel label="正在检查关注项" />
          ) : diagnostics.isError ? (
            <p className="overview-empty-copy">诊断不可用，当前无法判断关注项。</p>
          ) : attention.length ? (
            <ul className="overview-attention-list">
              {attention.map((item) => (
                <li key={item.title}>
                  <Badge tone={item.tone}>
                    {item.tone === 'error' ? '不可用' : item.tone === 'warning' ? '需检查' : '提示'}
                  </Badge>
                  <div>
                    <strong>{item.title}</strong>
                    <p>{item.detail}</p>
                    <Link to={item.href}>查看详情</Link>
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <p className="overview-empty-copy">
              当前诊断没有报告需处理项；这不等同于事件事实完整或业务指标正常。
            </p>
          )}
        </section>

        <section className="overview-panel" aria-labelledby="overview-investigate-title">
          <header>
            <div>
              <h2 id="overview-investigate-title">开始调查</h2>
              <p>按任务进入专用视图；高级语法留在查询工作台。</p>
            </div>
          </header>
          <nav className="overview-investigation-links" aria-label="调查入口">
            <Link to={route('heatmap')}>
              <strong>视口点击分布</strong>
              <span>按精确路由与视口检查 20 × 10 点击坐标格，不包含页面表面。</span>
            </Link>
            {project.permissions.includes('content.read') && (
              <Link to={route('session-replay')}>
                <strong>会话回放</strong>
                <span>选择会话，优先检查精确回放或安全快照。</span>
              </Link>
            )}
            <Link to={route('analysis')}>
              <strong>高级查询工作台</strong>
              <span>组合事件、主体、时间、过滤与高级分析模型。</span>
            </Link>
            <Link to={route('diagnostics')}>
              <strong>采集诊断</strong>
              <span>查看来源、预算、管道探针和拒绝原因明细。</span>
            </Link>
          </nav>
        </section>
      </div>

      {report && state?.proofs.length ? (
        <section className="overview-proof-panel" aria-labelledby="overview-proof-title">
          <header>
            <div>
              <h2 id="overview-proof-title">当前环境的最近管道探针</h2>
              <p>来自项目诊断 API 的保留窗口记录，不是事件活动日志或完整性水位。</p>
            </div>
            <Link to={route('diagnostics')}>查看全部诊断</Link>
          </header>
          <DataTable
            caption={`${environmentLabels[environment]}最近管道探针`}
            columns={proofColumns}
            rows={state.proofs.slice(0, 5)}
            rowKey={(row) => `${row.release_id}-${row.revision}`}
            minWidth={680}
            loading={diagnostics.isFetching}
          />
        </section>
      ) : null}
    </>
  );
}
