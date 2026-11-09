import type { Dashboard } from '@evia/api-client';
import {
  Badge,
  Button,
  DataSurface,
  DataTable,
  PaginationBar,
  type DataTableColumn,
} from '@evia/design-system';
import { formatTime } from '../analysis-model';
import { CatalogError } from '../catalog-helpers';
type DashboardsListProps = {
  items: Dashboard[];
  error: Error | null;
  loading: boolean;
  fetching: boolean;
  busy: boolean;
  canWrite: boolean;
  timezone: string;
  page: number;
  hasNext: boolean;
  onOpen: (analysis: Dashboard) => void;
  onRetry: () => void;
  onPrevious: () => void;
  onNext: () => void;
};
export function DashboardsList({
  items,
  error,
  loading,
  fetching,
  busy,
  canWrite,
  timezone,
  page,
  hasNext,
  onOpen,
  onRetry,
  onPrevious,
  onNext,
}: DashboardsListProps) {
  const columns: DataTableColumn<Dashboard>[] = [
    {
      key: 'title',
      header: '看板',
      render: (analysis) => (
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
      header: '更新时间',
      render: (analysis) => formatTime(analysis.updated_at, timezone),
    },
    {
      key: 'action',
      header: '操作',
      render: (analysis) => (
        <Button size="small" disabled={busy} onClick={() => onOpen(analysis)}>
          打开看板
        </Button>
      ),
    },
  ];
  return (
    <DataSurface label="保存的看板列表">
      <CatalogError error={error} retry={onRetry} />
      <DataTable
        caption="当前项目和环境的保存看板"
        columns={columns}
        rows={items}
        rowKey={(row) => row.dashboard_id}
        loading={loading}
        empty={
          canWrite ? '暂无保存的看板，新建看板后可保存配置并发布版本。' : '当前环境暂无保存的看板。'
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
    </DataSurface>
  );
}
