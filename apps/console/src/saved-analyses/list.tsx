import type { SavedAnalysis } from '@evia/api-client';
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
type SavedAnalysesListProps = {
  items: SavedAnalysis[];
  error: Error | null;
  loading: boolean;
  fetching: boolean;
  busy: boolean;
  canWrite: boolean;
  timezone: string;
  page: number;
  hasNext: boolean;
  onOpen: (analysis: SavedAnalysis) => void;
  onRetry: () => void;
  onPrevious: () => void;
  onNext: () => void;
};
export function SavedAnalysesList({
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
}: SavedAnalysesListProps) {
  const columns: DataTableColumn<SavedAnalysis>[] = [
    {
      key: 'title',
      header: '分析',
      render: (analysis) => (
        <Button variant="link" onClick={() => onOpen(analysis)} disabled={busy}>
          {analysis.title}
        </Button>
      ),
    },
    {
      key: 'kind',
      header: '类型',
      render: (analysis) => (analysis.kind === 'funnel' ? '有序漏斗' : '指标与趋势'),
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
          打开分析
        </Button>
      ),
    },
  ];
  return (
    <DataSurface label="保存的分析列表">
      <CatalogError error={error} retry={onRetry} />
      <DataTable
        caption="当前项目和环境的保存分析"
        columns={columns}
        rows={items}
        rowKey={(row) => row.analysis_id}
        loading={loading}
        empty={
          canWrite ? '暂无保存的分析，新建分析后可保存配置并发布版本。' : '当前环境暂无保存的分析。'
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
