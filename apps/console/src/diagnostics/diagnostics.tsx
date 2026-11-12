import { useQuery } from '@tanstack/react-query';
import { QuotaPanel } from './quota-panel';
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
  DataSurface,
  DataTable,
  EmptyPanel,
  PageHeading,
  type DataTableColumn,
} from '@evia/design-system';
import {
  featureLabel,
  formatAge,
  formatInstant,
  sectionStatusLabel,
  sectionStatusTone,
  shortUUID,
  totalRejectionCount,
} from './model';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

type NameCountRow = { name: string; count: number };
type EnvPolicyRow = Diagnostics['budget']['environments'][number];
type ProofRow = Diagnostics['pipeline']['proofs'][number];
type DisabledSourceRow = Diagnostics['sources']['disabled_sources'][number];
type WindowRow = Diagnostics['rejections']['windows'][number];

const countColumns: DataTableColumn<NameCountRow>[] = [
  { key: 'name', header: '名称', render: (row) => row.name },
  { key: 'count', header: '数量', render: (row) => String(row.count) },
];

export function DiagnosticsPage({ project, session }: PageProps) {
  const diagnostics = useQuery({
    queryKey: ['diagnostics', project.id],
    queryFn: ({ signal }) => api.getDiagnostics(project.id, signal),
  });

  if (diagnostics.isError) {
    return (
      <>
        <PageHeading title="采集诊断" />
        <Alert
          tone="error"
          title="无法读取采集诊断"
          action={<Button onClick={() => void diagnostics.refetch()}>重试</Button>}
        >
          {diagnostics.error instanceof ApiError ? diagnostics.error.message : '请稍后再试。'}
        </Alert>
      </>
    );
  }

  const report = diagnostics.data;

  const policyColumns: DataTableColumn<EnvPolicyRow>[] = [
    { key: 'environment', header: '环境', render: (row) => row.environment },
    {
      key: 'rate',
      header: '速率（事件/秒）',
      render: (row) => row.rate_per_second.toLocaleString('en-US'),
    },
    { key: 'burst', header: '突发容量', render: (row) => String(row.burst) },
    {
      key: 'override',
      header: '策略来源',
      render: (row) => (row.override ? '项目覆盖' : '默认策略'),
    },
  ];

  const proofColumns: DataTableColumn<ProofRow>[] = [
    { key: 'environment', header: '环境', render: (row) => row.environment },
    { key: 'release', header: '发布', render: (row) => shortUUID(row.release_id) },
    { key: 'revision', header: '修订版本', render: (row) => String(row.revision) },
    { key: 'observed', header: '最近探针时间', render: (row) => formatInstant(row.observed_at) },
    { key: 'age', header: '距今', render: (row) => formatAge(row.age_seconds) },
    { key: 'count', header: '探针次数', render: (row) => String(row.proof_count) },
  ];

  const disabledColumns: DataTableColumn<DisabledSourceRow>[] = [
    { key: 'name', header: '来源名称', render: (row) => row.name },
    { key: 'environment', header: '环境', render: (row) => row.environment },
    { key: 'kind', header: '类型', render: (row) => row.kind },
    { key: 'id', header: '标识', render: (row) => shortUUID(row.id) },
  ];

  const windowColumns: DataTableColumn<WindowRow>[] = [
    { key: 'bucket', header: '小时窗口（UTC）', render: (row) => formatInstant(row.bucket_at) },
    { key: 'environment', header: '环境', render: (row) => row.environment },
    {
      key: 'reasons',
      header: '拒绝原因',
      render: (row) => row.reasons.map((item) => `${item.reason} × ${item.count}`).join('，'),
    },
  ];

  return (
    <>
      <PageHeading title="采集诊断" />
      <div className="diagnostics-intro inline-notice">
        <Alert
          tone="info"
          title="诊断汇总项目的接入来源、Redis 采集预算、ClickHouse 管道探针与近 24 小时接收拒绝计数。所有分区独立降级，未启用的能力显示为“未启用”，不会用空数据冒充健康。"
        />
      </div>

      <DataSurface label="功能开关">
        {report ? (
          <dl className="diagnostics-features">
            <div>
              <dt>实时采集</dt>
              <dd>
                <Badge tone={report.features.collection_enabled ? 'success' : 'neutral'}>
                  {featureLabel(report.features.collection_enabled)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt>分析查询</dt>
              <dd>
                <Badge tone={report.features.analysis_enabled ? 'success' : 'neutral'}>
                  {featureLabel(report.features.analysis_enabled)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt>发布探针</dt>
              <dd>
                <Badge tone={report.features.delivery_enabled ? 'success' : 'neutral'}>
                  {featureLabel(report.features.delivery_enabled)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt>生成时间</dt>
              <dd>{formatInstant(report.generated_at)}</dd>
            </div>
          </dl>
        ) : (
          <EmptyPanel description="正在读取诊断数据。" />
        )}
      </DataSurface>

      <DataSurface label="接入来源">
        {report ? (
          <>
            <div className="diagnostics-heading">
              <Badge tone={sectionStatusTone(report.sources.status)}>
                {sectionStatusLabel(report.sources.status)}
              </Badge>
              <span className="evia-field-hint">
                来源总数 {report.sources.total}
                {report.sources.truncated ? '（达到单页上限，仅展示前 200 条）' : ''}
              </span>
            </div>
            <DataTable
              caption="来源状态分布"
              columns={countColumns}
              rows={report.sources.by_status}
              rowKey={(row) => `status-${row.name}`}
              minWidth={320}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="暂无来源。" />}
            />
            <DataTable
              caption="来源环境分布"
              columns={countColumns}
              rows={report.sources.by_environment}
              rowKey={(row) => `env-${row.name}`}
              minWidth={320}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="暂无来源。" />}
            />
            <DataTable
              caption="已停用来源"
              columns={disabledColumns}
              rows={report.sources.disabled_sources}
              rowKey={(row) => row.id}
              minWidth={560}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="没有已停用来源。" />}
            />
          </>
        ) : (
          <EmptyPanel description="正在读取来源清单。" />
        )}
      </DataSurface>

      <DataSurface label="采集预算">
        {report ? (
          <>
            <div className="diagnostics-heading">
              <Badge tone={sectionStatusTone(report.budget.status)}>
                {sectionStatusLabel(report.budget.status)}
              </Badge>
              {report.budget.error_code ? (
                <span className="evia-field-hint">错误码：{report.budget.error_code}</span>
              ) : null}
            </div>
            <DataTable
              caption="各环境有效预算策略"
              columns={policyColumns}
              rows={report.budget.environments}
              rowKey={(row) => row.environment}
              minWidth={560}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="预算能力未启用。" />}
            />
          </>
        ) : (
          <EmptyPanel description="正在读取预算策略。" />
        )}
      </DataSurface>

      <QuotaPanel project={project} session={session} />

      <DataSurface label="管道就绪探针">
        {report ? (
          <>
            <div className="diagnostics-heading">
              <Badge tone={sectionStatusTone(report.pipeline.status)}>
                {sectionStatusLabel(report.pipeline.status)}
              </Badge>
              {report.pipeline.error_code ? (
                <span className="evia-field-hint">错误码：{report.pipeline.error_code}</span>
              ) : null}
            </div>
            <DataTable
              caption="最近管道就绪证据"
              columns={proofColumns}
              rows={report.pipeline.proofs}
              rowKey={(row) => `${row.environment}-${row.release_id}-${row.revision}`}
              minWidth={760}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="保留窗口内没有探针收据。" />}
            />
          </>
        ) : (
          <EmptyPanel description="正在读取管道探针。" />
        )}
      </DataSurface>

      <DataSurface label="接收拒绝统计（近 24 小时）">
        {report ? (
          <>
            <div className="diagnostics-heading">
              <Badge tone={sectionStatusTone(report.rejections.status)}>
                {sectionStatusLabel(report.rejections.status)}
              </Badge>
              <span className="evia-field-hint">
                拒绝事件总数 {totalRejectionCount(report)}
                {report.rejections.error_code ? `；错误码：${report.rejections.error_code}` : ''}
              </span>
            </div>
            <DataTable
              caption="拒绝原因汇总"
              columns={countColumns}
              rows={report.rejections.totals.map((item) => ({
                name: item.reason,
                count: item.count,
              }))}
              rowKey={(row) => `total-${row.name}`}
              minWidth={320}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="近 24 小时没有来源可归属的拒绝记录。" />}
            />
            <DataTable
              caption="按小时拒绝明细"
              columns={windowColumns}
              rows={report.rejections.windows}
              rowKey={(row) => `${row.bucket_at}-${row.environment}`}
              minWidth={720}
              loading={diagnostics.isFetching}
              empty={<EmptyPanel description="没有小时级拒绝明细。" />}
            />
          </>
        ) : (
          <EmptyPanel description="正在读取拒绝统计。" />
        )}
      </DataSurface>
    </>
  );
}
