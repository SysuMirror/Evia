import { useEffect, useState } from 'react';
import type { Dashboard, DashboardFolder } from '@evia/api-client';
import {
  Badge,
  Button,
  DataSurface,
  DataTable,
  Modal,
  PaginationBar,
  type DataTableColumn,
} from '@evia/design-system';
import { formatTime } from '../analysis-model';
import { CatalogError } from '../catalog-helpers';
import type { DashboardListView } from './use-history';
import { MoveDashboardModal } from './move-dashboard';

type DashboardsListProps = {
  items: Dashboard[];
  folders: DashboardFolder[];
  error: Error | null;
  loading: boolean;
  fetching: boolean;
  busy: boolean;
  busyLabel: string;
  canWrite: boolean;
  timezone: string;
  page: number;
  hasNext: boolean;
  view: DashboardListView;
  onViewChange: (view: DashboardListView) => void;
  onOpen: (analysis: Dashboard) => void;
  onArchive: (analysis: Dashboard) => void;
  onUnarchive: (analysis: Dashboard) => void;
  onMove: (analysis: Dashboard, folderID: string) => void;
  onRetry: () => void;
  onPrevious: () => void;
  onNext: () => void;
};
export function DashboardsList({
  items,
  folders,
  error,
  loading,
  fetching,
  busy,
  busyLabel,
  canWrite,
  timezone,
  page,
  hasNext,
  view,
  onViewChange,
  onOpen,
  onArchive,
  onUnarchive,
  onMove,
  onRetry,
  onPrevious,
  onNext,
}: DashboardsListProps) {
  const [pendingArchive, setPendingArchive] = useState<Dashboard | null>(null);
  const [archiveSubmitted, setArchiveSubmitted] = useState(false);
  const [pendingMove, setPendingMove] = useState<Dashboard | null>(null);
  const archivedView = view === 'archived';
  useEffect(() => {
    if (archiveSubmitted && busyLabel !== '归档看板') {
      setPendingArchive(null);
      setArchiveSubmitted(false);
    }
  }, [busyLabel, archiveSubmitted]);
  const columns: DataTableColumn<Dashboard>[] = [
    {
      key: 'title',
      header: '看板',
      render: (analysis) =>
        archivedView ? (
          <span>{analysis.title}</span>
        ) : (
          <Button variant="link" onClick={() => onOpen(analysis)} disabled={busy}>
            {analysis.title}
          </Button>
        ),
    },
    {
      key: 'version',
      header: '发布状态',
      render: (analysis) => (
        <Badge tone={analysis.published_version ? 'success' : 'neutral'}>
          {analysis.published_version ? `已发布 v${analysis.published_version}` : '尚未发布'}
        </Badge>
      ),
    },
    { key: 'draft', header: '草稿修订', render: (analysis) => `r${analysis.draft_revision}` },
    {
      key: 'updated',
      header: archivedView ? '归档时间' : '更新时间',
      render: (analysis) => formatTime(analysis.archived_at ?? analysis.updated_at, timezone),
    },
    {
      key: 'action',
      header: '操作',
      render: (analysis) =>
        archivedView ? (
          <Button size="small" disabled={busy || !canWrite} onClick={() => onUnarchive(analysis)}>
            恢复看板
          </Button>
        ) : (
          <span className="dashboard-row-actions">
            <Button size="small" disabled={busy} onClick={() => onOpen(analysis)}>
              打开看板
            </Button>
            <Button
              size="small"
              disabled={busy || !canWrite}
              onClick={() => setPendingMove(analysis)}
            >
              移动
            </Button>
            <Button
              size="small"
              variant="danger"
              disabled={busy || !canWrite}
              onClick={() => {
                setArchiveSubmitted(false);
                setPendingArchive(analysis);
              }}
            >
              归档
            </Button>
          </span>
        ),
    },
  ];
  return (
    <DataSurface label="保存的看板列表">
      <div className="dashboard-view-switch" role="group" aria-label="看板视图切换">
        <Button
          size="small"
          variant={archivedView ? 'ghost' : 'secondary'}
          aria-pressed={!archivedView}
          disabled={busy}
          onClick={() => onViewChange('active')}
        >
          活跃看板
        </Button>
        <Button
          size="small"
          variant={archivedView ? 'secondary' : 'ghost'}
          aria-pressed={archivedView}
          disabled={busy}
          onClick={() => onViewChange('archived')}
        >
          已归档
        </Button>
      </div>
      <CatalogError error={error} retry={onRetry} />
      <DataTable
        caption={archivedView ? '当前项目和环境的已归档看板' : '当前项目和环境的保存看板'}
        columns={columns}
        rows={items}
        rowKey={(row) => row.dashboard_id}
        loading={loading}
        empty={
          archivedView
            ? canWrite
              ? '暂无已归档看板；归档后的看板会移至此处，可随时恢复。'
              : '当前环境暂无已归档看板。'
            : canWrite
              ? '暂无保存的看板，新建看板后可保存配置并发布版本。'
              : '当前环境暂无保存的看板。'
        }
      />
      <PaginationBar
        page={page}
        count={items.length}
        hasNext={hasNext}
        busy={fetching || busy}
        onPrevious={onPrevious}
        onNext={onNext}
      />
      <Modal
        open={!!pendingArchive}
        title="确认归档看板"
        busy={busyLabel === '归档看板'}
        onRequestClose={() => {
          if (!busy) setPendingArchive(null);
        }}
        footer={
          <>
            <Button disabled={busy} onClick={() => setPendingArchive(null)}>
              取消
            </Button>
            <Button
              variant="danger"
              loading={busyLabel === '归档看板'}
              disabled={!pendingArchive || (busy && busyLabel !== '归档看板')}
              onClick={() => {
                if (pendingArchive) {
                  setArchiveSubmitted(true);
                  onArchive(pendingArchive);
                }
              }}
            >
              确认归档
            </Button>
          </>
        }
      >
        <p>
          归档后，看板「<strong>{pendingArchive?.title}</strong>
          」将从默认列表隐藏，且草稿保存、发布与运行入口全部关闭；已发布版本与审计记录保留，可在「已归档」视图中恢复。
        </p>
      </Modal>
      <MoveDashboardModal
        dashboard={pendingMove}
        folders={folders}
        busy={busy}
        onClose={() => {
          if (!busy) setPendingMove(null);
        }}
        onMove={(dashboard, folderID) => {
          onMove(dashboard, folderID);
          setPendingMove(null);
        }}
      />
    </DataSurface>
  );
}
