import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, ApiError, type Project, type Session } from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  PaginationBar,
  type DataTableColumn,
} from '@evia/design-system';
import type { MetricVersion } from '@evia/api-client';
import { formatMetricInstant, opLabels } from './model';

interface VersionsPanelProps {
  project: Project;
  session: Session;
  metricID: string;
  selectedVersion: number;
  onSelectVersion: (version: number) => void;
}

export function MetricVersionsPanel({
  project,
  session,
  metricID,
  selectedVersion,
  onSelectVersion,
}: VersionsPanelProps) {
  const [cursors, setCursors] = useState<string[]>([]);
  const versions = useQuery({
    queryKey: ['metrics', project.id, session.user_id, metricID, 'versions', cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listMetricVersions(project.id, metricID, { limit: 20, cursor: cursors.at(-1) }, signal),
  });

  const columns: DataTableColumn<MetricVersion>[] = [
    {
      key: 'version',
      header: '版本',
      render: (row) => (
        <Button variant="link" onClick={() => onSelectVersion(row.version)}>
          v{row.version}
        </Button>
      ),
    },
    { key: 'title', header: '标题', render: (row) => row.definition.title },
    {
      key: 'aggregation',
      header: '口径',
      render: (row) => (
        <Badge>{opLabels[row.definition.aggregation.op] ?? row.definition.aggregation.op}</Badge>
      ),
    },
    { key: 'unit', header: '单位', render: (row) => row.definition.unit },
    { key: 'created', header: '发布时间', render: (row) => formatMetricInstant(row.created_at) },
  ];

  if (versions.isPending) return <DataSurface label="指标版本">正在加载版本…</DataSurface>;
  if (versions.isError)
    return (
      <Alert
        tone="error"
        title="无法读取版本"
        action={<Button onClick={() => void versions.refetch()}>重试</Button>}
      >
        {versions.error instanceof ApiError ? versions.error.message : '请稍后再试。'}
      </Alert>
    );

  return (
    <DataSurface label="指标版本">
      <DataTable
        caption="指标已发布版本"
        columns={columns}
        rows={versions.data.items}
        rowKey={(row) => String(row.version)}
        minWidth={640}
        loading={versions.isFetching}
        empty={<EmptyPanel description="还没有已发布版本，请先在草稿分区校验并发布。" />}
      />
      <PaginationBar
        page={cursors.length + 1}
        count={versions.data.items.length}
        hasNext={!!versions.data.next_cursor}
        busy={versions.isFetching}
        onPrevious={() => setCursors((current) => current.slice(0, -1))}
        onNext={() => {
          if (versions.data.next_cursor)
            setCursors((current) => [...current, versions.data!.next_cursor!]);
        }}
      />
      {selectedVersion > 0 && (
        <p className="evia-field-hint">运行分区当前使用版本 v{selectedVersion}。</p>
      )}
    </DataSurface>
  );
}
