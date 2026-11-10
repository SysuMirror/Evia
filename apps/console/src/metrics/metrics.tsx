import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type MetricRecord,
  type Project,
  type Session,
} from '@evia/api-client';
import {
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  ErrorPanel,
  LoadingPanel,
  PageHeading,
  PaginationBar,
  Panel,
  Tabs,
  type DataTableColumn,
} from '@evia/design-system';
import { MetricEditor } from './editor';
import { MetricVersionsPanel } from './versions';
import { MetricRunPanel } from './run';
import { useMetricsRequest } from './use-metrics-request';
import { formatMetricInstant } from './model';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

export function MetricsPage({ project, session, environment }: PageProps) {
  const canRead = project.permissions.includes('metrics.read');
  const canWrite = project.permissions.includes('metrics.write');
  const canPublish = project.permissions.includes('metrics.publish');
  const canRun = project.permissions.includes('metrics.run');
  const requests = useMetricsRequest(
    project,
    session,
    `${project.id}:${environment}:${session.user_id}:${session.csrf_token}`,
    canWrite || canPublish,
  );
  const [metricID, setMetricID] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [tab, setTab] = useState('draft');
  const [runVersion, setRunVersion] = useState(0);
  const [cursors, setCursors] = useState<string[]>([]);

  const list = useQuery({
    queryKey: ['metrics', project.id, session.user_id, environment, 'list', cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listMetrics(project.id, { environment, limit: 20, cursor: cursors.at(-1) }, signal),
    enabled: canRead && !metricID && !creating,
  });

  const current = useQuery({
    queryKey: ['metrics', project.id, session.user_id, environment, metricID, 'summary'],
    queryFn: ({ signal }) => api.getMetric(project.id, metricID!, signal),
    enabled: !!metricID,
  });

  if (!canRead)
    return (
      <>
        <PageHeading title="独立指标" />
        <ErrorPanel message="你没有读取指标的权限。" />
      </>
    );
  if (requests.denied)
    return (
      <>
        <PageHeading title="独立指标" />
        <ErrorPanel message={requests.denied.message} />
      </>
    );

  const openMetric = (id: string) => {
    setMetricID(id);
    setCreating(false);
    setTab('draft');
  };

  if (creating)
    return (
      <div className="metrics-workspace section-stack">
        <PageHeading
          title="新建指标"
          action={
            <Button
              onClick={() => {
                setCreating(false);
              }}
            >
              返回列表
            </Button>
          }
        />
        <MetricEditor
          project={project}
          session={session}
          environment={environment}
          canWrite={canWrite}
          canPublish={canPublish}
          perform={requests.perform}
          busy={requests.busy}
          onCreated={(id) => openMetric(id)}
        />
      </div>
    );

  if (metricID) {
    const published = current.data?.published_version ?? 0;
    return (
      <div className="metrics-workspace section-stack">
        <PageHeading
          title={current.data?.definition?.title ?? '指标工作区'}
          action={<Button onClick={() => setMetricID(undefined)}>返回列表</Button>}
        />
        {current.isPending ? (
          <LoadingPanel label="正在加载指标" />
        ) : current.isError ? (
          <ErrorPanel
            message={current.error instanceof ApiError ? current.error.message : '指标读取失败'}
            retry={() => void current.refetch()}
          />
        ) : (
          <Panel className="metrics-tabs">
            <Tabs
              label="指标分区"
              value={tab}
              onChange={setTab}
              items={[
                { id: 'draft', label: '草稿与发布', content: null },
                { id: 'versions', label: '已发布版本', content: null, disabled: published < 1 },
                { id: 'run', label: '运行', content: null, disabled: published < 1 },
              ].map((item) => ({
                ...item,
                content:
                  item.id === 'draft' ? (
                    <MetricEditor
                      project={project}
                      session={session}
                      environment={environment}
                      metricID={metricID}
                      canWrite={canWrite}
                      canPublish={canPublish}
                      perform={requests.perform}
                      busy={requests.busy}
                      onCreated={openMetric}
                    />
                  ) : item.id === 'versions' ? (
                    <MetricVersionsPanel
                      project={project}
                      session={session}
                      metricID={metricID}
                      selectedVersion={runVersion || published}
                      onSelectVersion={(version) => {
                        setRunVersion(version);
                        setTab('run');
                      }}
                    />
                  ) : (
                    <MetricRunPanel
                      project={project}
                      session={session}
                      metricID={metricID}
                      version={runVersion || published}
                      canRun={canRun}
                    />
                  ),
              }))}
            />
          </Panel>
        )}
      </div>
    );
  }

  const columns: DataTableColumn<MetricRecord>[] = [
    {
      key: 'title',
      header: '指标',
      render: (row) => (
        <Button variant="link" onClick={() => openMetric(row.metric_id)}>
          {row.title}
        </Button>
      ),
    },
    {
      key: 'published',
      header: '已发布版本',
      render: (row) => (
        <Badge tone={row.published_version ? 'success' : 'neutral'}>v{row.published_version}</Badge>
      ),
    },
    { key: 'draft', header: '草稿修订', render: (row) => row.draft_revision },
    { key: 'updated', header: '更新时间', render: (row) => formatMetricInstant(row.updated_at) },
  ];

  return (
    <div className="metrics-workspace section-stack">
      <PageHeading
        title="独立指标"
        action={
          canWrite ? (
            <Button variant="primary" onClick={() => setCreating(true)} disabled={!!requests.busy}>
              新建指标
            </Button>
          ) : undefined
        }
      />
      <DataSurface label="指标列表">
        {list.isPending ? (
          <LoadingPanel label="正在加载指标" />
        ) : list.isError ? (
          <ErrorPanel
            message={list.error instanceof ApiError ? list.error.message : '指标列表读取失败'}
            retry={() => void list.refetch()}
          />
        ) : (
          <>
            <DataTable
              caption="指标列表"
              columns={columns}
              rows={list.data?.items ?? []}
              rowKey={(row) => row.metric_id}
              minWidth={620}
              loading={list.isFetching}
              empty={<EmptyPanel description="还没有指标，新建一个原子指标来固定统计口径。" />}
            />
            <PaginationBar
              page={cursors.length + 1}
              count={list.data?.items.length ?? 0}
              hasNext={!!list.data?.next_cursor}
              busy={list.isFetching || !!requests.busy}
              onPrevious={() => setCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (list.data?.next_cursor)
                  setCursors((current) => [...current, list.data!.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>
    </div>
  );
}
