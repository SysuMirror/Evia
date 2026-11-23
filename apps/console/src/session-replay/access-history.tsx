import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type ProjectAuditEntry,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  PaginationBar,
  ResultHeader,
  Select,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import {
  contentAccessResourcePrefix,
  parseContentAccessResource,
  replayInstant,
  shortReplayID,
} from './model';

const ACCESS_ACTION = 'content.item.read';
const HISTORY_LIMIT = 20;
const HISTORY_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;

type OutcomeFilter = '' | ProjectAuditEntry['outcome'];
type ScopeFilter = 'environment' | 'stream';

type AccessHistoryProps = {
  project: Project;
  environment: Environment;
  streamID?: string;
  active: boolean;
  refreshToken: number;
};

export function ReplayAccessHistory({
  project,
  environment,
  streamID,
  active,
  refreshToken,
}: AccessHistoryProps) {
  const [outcome, setOutcome] = useState<OutcomeFilter>('');
  const [scope, setScope] = useState<ScopeFilter>('stream');
  const [cursors, setCursors] = useState<string[]>([]);
  const [windowEnd, setWindowEnd] = useState(() => Date.now());

  useEffect(() => {
    setCursors([]);
    setWindowEnd((current) => Math.max(Date.now(), current + 1));
  }, [refreshToken]);
  const effectiveScope = scope === 'stream' && streamID ? 'stream' : 'environment';
  const audit = useQuery({
    queryKey: [
      'replay-access-history',
      project.id,
      environment,
      effectiveScope,
      effectiveScope === 'stream' ? streamID : '',
      outcome,
      windowEnd,
      cursors.at(-1) ?? '',
    ],
    queryFn: ({ signal }) =>
      api.listAuditLogs(
        project.id,
        {
          from: new Date(windowEnd - HISTORY_WINDOW_MS).toISOString(),
          to: new Date(windowEnd + 60_000).toISOString(),
          action: ACCESS_ACTION,
          environment,
          outcome: outcome || undefined,
          resourceKeyPrefix:
            effectiveScope === 'stream' && streamID
              ? contentAccessResourcePrefix(streamID)
              : undefined,
          cursor: cursors.at(-1),
          limit: HISTORY_LIMIT,
        },
        signal,
      ),
    enabled: active,
  });

  const columns: DataTableColumn<ProjectAuditEntry>[] = [
    { key: 'time', header: '访问时间', render: (row) => replayInstant(row.occurred_at) },
    {
      key: 'actor',
      header: '访问主体',
      render: (row) => (
        <span className="replay-access-actor">
          <Badge>{row.actor_kind === 'machine' ? '机器' : '人员'}</Badge>
          <code title={row.actor_id}>{shortReplayID(row.actor_id)}</code>
          {row.credential_id && (
            <span className="replay-access-credential">
              凭据 <code title={row.credential_id}>{shortReplayID(row.credential_id)}</code>
            </span>
          )}
        </span>
      ),
    },
    {
      key: 'resource',
      header: '内容资源',
      render: (row) => {
        const resource = parseContentAccessResource(row.resource_key);
        return resource.itemID ? (
          <span className="replay-access-resource">
            <span>
              会话 <code title={resource.streamID}>{shortReplayID(resource.streamID ?? '—')}</code>
            </span>
            <span>
              内容项 <code title={resource.itemID}>{shortReplayID(resource.itemID)}</code>
            </span>
            <span>{resource.contentType ?? '未知内容类型'}</span>
          </span>
        ) : (
          <code title={row.resource_key}>{shortReplayID(row.resource_key)}</code>
        );
      },
    },
    {
      key: 'outcome',
      header: '结果',
      render: (row) => (
        <Badge tone={row.outcome === 'success' ? 'success' : 'error'}>
          {row.outcome === 'success' ? '已返回' : '未返回'}
        </Badge>
      ),
    },
  ];

  return (
    <div className="replay-access-panel">
      <DataSurface label="敏感内容访问历史">
        <ResultHeader
          title="访问历史"
          summary={`最近 7 天，${environment} 环境`}
          action={
            <Button
              loading={audit.isFetching}
              onClick={() => {
                setCursors([]);
                setWindowEnd((current) => Math.max(Date.now(), current + 1));
              }}
            >
              刷新
            </Button>
          }
        />
        <Toolbar label="访问历史筛选">
          <div className="replay-access-filters">
            <Field label="资源范围" htmlFor="replay-access-scope">
              <Select
                id="replay-access-scope"
                value={effectiveScope}
                options={[
                  { value: 'environment', label: '当前环境全部内容' },
                  {
                    value: 'stream',
                    label: streamID ? '当前会话' : '当前会话（请先选择）',
                    disabled: !streamID,
                  },
                ]}
                onChange={(event) => {
                  setScope(event.target.value as ScopeFilter);
                  setCursors([]);
                  setWindowEnd((current) => Math.max(Date.now(), current + 1));
                }}
              />
            </Field>
            <Field label="访问结果" htmlFor="replay-access-outcome">
              <Select
                id="replay-access-outcome"
                value={outcome}
                options={[
                  { value: '', label: '全部结果' },
                  { value: 'success', label: '已返回' },
                  { value: 'failure', label: '未返回' },
                ]}
                onChange={(event) => {
                  setOutcome(event.target.value as OutcomeFilter);
                  setCursors([]);
                  setWindowEnd((current) => Math.max(Date.now(), current + 1));
                }}
              />
            </Field>
          </div>
        </Toolbar>
        {audit.isError ? (
          <Alert
            tone="error"
            title="无法读取访问历史"
            action={<Button onClick={() => void audit.refetch()}>重试</Button>}
          >
            {audit.error instanceof ApiError ? audit.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <>
            <div className="replay-access-scroll">
              <DataTable
                caption="敏感内容访问审计记录"
                columns={columns}
                rows={audit.data?.items ?? []}
                rowKey={(row) => row.id}
                minWidth={680}
                loading={audit.isFetching}
                empty={<EmptyPanel description="当前筛选范围内没有内容访问记录。" />}
              />
            </div>
            <PaginationBar
              page={cursors.length + 1}
              count={audit.data?.items.length ?? 0}
              hasNext={!!audit.data?.next_cursor}
              busy={audit.isFetching}
              onPrevious={() => setCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (audit.data?.next_cursor)
                  setCursors((current) => [...current, audit.data!.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>
    </div>
  );
}
