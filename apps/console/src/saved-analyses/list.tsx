import { useEffect, useState } from 'react';
import type { SavedAnalysis } from '@evia/api-client';
import {
  Badge,
  Button,
  DataSurface,
  DataTable,
  Input,
  Modal,
  PaginationBar,
  type DataTableColumn,
} from '@evia/design-system';
import { formatTime } from '../analysis-model';
import { CatalogError } from '../catalog-helpers';
import type { SavedAnalysisListView } from './use-history';
import './styles.css';

type SavedAnalysesListProps = {
  items: SavedAnalysis[];
  error: Error | null;
  loading: boolean;
  fetching: boolean;
  busy: boolean;
  busyLabel: string;
  canWrite: boolean;
  timezone: string;
  page: number;
  hasNext: boolean;
  view: SavedAnalysisListView;
  onViewChange: (view: SavedAnalysisListView) => void;
  search: string;
  onSearchChange: (term: string) => void;
  onOpen: (analysis: SavedAnalysis) => void;
  onArchive: (analysis: SavedAnalysis) => void;
  onUnarchive: (analysis: SavedAnalysis) => void;
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
  busyLabel,
  canWrite,
  timezone,
  page,
  hasNext,
  view,
  onViewChange,
  search,
  onSearchChange,
  onOpen,
  onArchive,
  onUnarchive,
  onRetry,
  onPrevious,
  onNext,
}: SavedAnalysesListProps) {
  const [pendingArchive, setPendingArchive] = useState<SavedAnalysis | null>(null);
  const [archiveSubmitted, setArchiveSubmitted] = useState(false);
  const archivedView = view === 'archived';
  useEffect(() => {
    if (archiveSubmitted && busyLabel !== '归档分析') {
      setPendingArchive(null);
      setArchiveSubmitted(false);
    }
  }, [busyLabel, archiveSubmitted]);
  const columns: DataTableColumn<SavedAnalysis>[] = [
    {
      key: 'title',
      header: '分析',
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
      header: archivedView ? '归档时间' : '更新时间',
      render: (analysis) => formatTime(analysis.archived_at ?? analysis.updated_at, timezone),
    },
    {
      key: 'action',
      header: '操作',
      kind: 'actions',
      render: (analysis) =>
        archivedView ? (
          <Button size="small" disabled={busy || !canWrite} onClick={() => onUnarchive(analysis)}>
            恢复分析
          </Button>
        ) : (
          <span className="saved-analysis-row-actions">
            <Button size="small" disabled={busy} onClick={() => onOpen(analysis)}>
              打开分析
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
    <DataSurface label="保存的分析列表">
      <div className="saved-analysis-list-toolbar">
        <div className="saved-analysis-view-switch" role="group" aria-label="分析视图切换">
          <Button
            size="small"
            variant={archivedView ? 'ghost' : 'secondary'}
            aria-pressed={!archivedView}
            disabled={busy}
            onClick={() => onViewChange('active')}
          >
            活跃分析
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
        <Input
          className="saved-analysis-search-input"
          type="search"
          aria-label="搜索分析标题"
          placeholder="按标题搜索…"
          maxLength={128}
          value={search}
          onChange={(event) => onSearchChange(event.target.value)}
        />
      </div>
      <CatalogError error={error} retry={onRetry} />
      <DataTable
        caption={archivedView ? '当前项目和环境的已归档分析' : '当前项目和环境的保存分析'}
        columns={columns}
        rows={items}
        rowKey={(row) => row.analysis_id}
        loading={loading}
        empty={
          archivedView
            ? canWrite
              ? '暂无已归档分析；归档后的分析会移至此处，可随时恢复。'
              : '当前环境暂无已归档分析。'
            : canWrite
              ? '暂无保存的分析，新建分析后可保存配置并发布版本。'
              : '当前环境暂无保存的分析。'
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
        title="确认归档分析"
        busy={busyLabel === '归档分析'}
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
              loading={busyLabel === '归档分析'}
              disabled={!pendingArchive || (busy && busyLabel !== '归档分析')}
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
          归档后，分析「<strong>{pendingArchive?.title}</strong>
          」将从默认列表隐藏，且草稿保存、校验、发布与运行入口全部关闭；已发布的不可变版本、依赖见证与审计记录保留，看板/报告对其历史版本的引用继续有效，可在「已归档」视图中恢复。
        </p>
      </Modal>
    </DataSurface>
  );
}
