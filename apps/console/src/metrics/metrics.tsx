import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
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
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Panel,
  Tabs,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { MetricEditor } from './editor';
import { MetricVersionsPanel } from './versions';
import { MetricRunPanel } from './run';
import { useMetricsRequest } from './use-metrics-request';
import { formatMetricInstant, normalizeMetricSearch } from './model';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };
type MetricListView = 'active' | 'archived';

/** Debounce window for the title search box so typing never fires a request per keystroke. */
const searchDebounceMs = 300;

export function MetricsPage({ project, session, environment }: PageProps) {
  const canRead = project.permissions.includes('metrics.read');
  const canWrite = project.permissions.includes('metrics.write');
  const canPublish = project.permissions.includes('metrics.publish');
  const canRun = project.permissions.includes('metrics.run');
  const toast = useToast();
  const queryClient = useQueryClient();
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
  const [archiveView, setArchiveView] = useState<MetricListView>('active');
  // searchInput mirrors the textbox immediately; search is the debounced,
  // normalized term actually bound to the request and to any page cursor.
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [pendingArchive, setPendingArchive] = useState<MetricRecord | null>(null);
  const [archiveSubmitted, setArchiveSubmitted] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => {
      const next = normalizeMetricSearch(searchInput);
      setSearch((previous) => (next === previous ? previous : next));
    }, searchDebounceMs);
    return () => clearTimeout(timer);
  }, [searchInput]);
  // A new predicate invalidates any cursor collected under the previous term.
  useEffect(() => {
    setCursors([]);
  }, [search]);

  const list = useQuery({
    queryKey: [
      'metrics',
      project.id,
      session.user_id,
      environment,
      'list',
      archiveView,
      search,
      cursors.at(-1) ?? '',
    ],
    queryFn: ({ signal }) =>
      api.listMetrics(
        project.id,
        {
          environment,
          view: archiveView,
          search: search || undefined,
          limit: 20,
          cursor: cursors.at(-1),
        },
        signal,
      ),
    enabled: canRead && !metricID && !creating,
  });

  useEffect(() => {
    if (archiveSubmitted && requests.busy !== '指标归档') {
      setPendingArchive(null);
      setArchiveSubmitted(false);
    }
  }, [requests.busy, archiveSubmitted]);

  const invalidateList = () =>
    void queryClient.invalidateQueries({ queryKey: ['metrics', project.id] });

  const archive = (metric: MetricRecord) => {
    void requests.perform(
      '指标归档',
      (signal) => api.archiveMetric(project.id, metric.metric_id, session.csrf_token, signal),
      () => {
        toast.success(`指标「${metric.title}」已归档，可在已归档视图中恢复。`);
        invalidateList();
      },
    );
  };
  const unarchive = (metric: MetricRecord) => {
    void requests.perform(
      '指标恢复',
      (signal) => api.unarchiveMetric(project.id, metric.metric_id, session.csrf_token, signal),
      () => {
        toast.success(`指标「${metric.title}」已恢复到活跃列表。`);
        invalidateList();
      },
    );
  };

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

  const archivedView = archiveView === 'archived';
  const columns: DataTableColumn<MetricRecord>[] = [
    {
      key: 'title',
      header: '指标',
      render: (row) =>
        archivedView ? (
          <span>{row.title}</span>
        ) : (
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
    {
      key: 'updated',
      header: archivedView ? '归档时间' : '更新时间',
      render: (row) => formatMetricInstant(row.archived_at ?? row.updated_at),
    },
    {
      key: 'actions',
      header: '操作',
      kind: 'actions',
      render: (row) =>
        canWrite ? (
          archivedView ? (
            <Button size="small" disabled={!!requests.busy} onClick={() => unarchive(row)}>
              恢复指标
            </Button>
          ) : (
            <Button
              size="small"
              variant="danger"
              disabled={!!requests.busy}
              onClick={() => {
                setArchiveSubmitted(false);
                setPendingArchive(row);
              }}
            >
              归档
            </Button>
          )
        ) : (
          <span className="evia-field-hint">只读</span>
        ),
    },
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
        <div className="metric-list-toolbar">
          <div className="metric-view-switch" role="group" aria-label="指标视图切换">
            <Button
              size="small"
              variant={archivedView ? 'ghost' : 'secondary'}
              aria-pressed={!archivedView}
              disabled={!!requests.busy}
              onClick={() => {
                setArchiveView('active');
                setCursors([]);
              }}
            >
              活跃指标
            </Button>
            <Button
              size="small"
              variant={archivedView ? 'secondary' : 'ghost'}
              aria-pressed={archivedView}
              disabled={!!requests.busy}
              onClick={() => {
                setArchiveView('archived');
                setCursors([]);
              }}
            >
              已归档
            </Button>
          </div>
          <Input
            className="metric-search-input"
            type="search"
            aria-label="搜索指标标题"
            placeholder="按标题搜索…"
            maxLength={128}
            value={searchInput}
            onChange={(event) => setSearchInput(event.target.value)}
          />
        </div>
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
              caption={archivedView ? '当前项目和环境的已归档指标' : '指标列表'}
              columns={columns}
              rows={list.data?.items ?? []}
              rowKey={(row) => row.metric_id}
              minWidth={620}
              loading={list.isFetching}
              empty={
                <EmptyPanel
                  description={
                    archivedView
                      ? '暂无已归档指标；归档后的指标会移至此处，可随时恢复。'
                      : '还没有指标，新建一个原子指标来固定统计口径。'
                  }
                />
              }
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
      <Modal
        open={!!pendingArchive}
        title="确认归档指标"
        busy={requests.busy === '指标归档'}
        onRequestClose={() => {
          if (!requests.busy) setPendingArchive(null);
        }}
        footer={
          <>
            <Button disabled={!!requests.busy} onClick={() => setPendingArchive(null)}>
              取消
            </Button>
            <Button
              variant="danger"
              loading={requests.busy === '指标归档'}
              disabled={!pendingArchive || (!!requests.busy && requests.busy !== '指标归档')}
              onClick={() => {
                if (pendingArchive) {
                  setArchiveSubmitted(true);
                  archive(pendingArchive);
                }
              }}
            >
              确认归档
            </Button>
          </>
        }
      >
        <p>
          归档后，指标「<strong>{pendingArchive?.title}</strong>
          」将从默认列表隐藏，且草稿保存、校验、发布与运行入口全部关闭；已发布的不可变版本、依赖见证与审计记录保留，保存分析的指标引用与复合公式指标对其历史版本的引用继续有效，可在「已归档」视图中恢复。
        </p>
      </Modal>
    </div>
  );
}
