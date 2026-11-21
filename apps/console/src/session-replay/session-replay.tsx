import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type ContentItem,
  type ContentItemSummary,
  type ContentStreamSummary,
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
  PaginationBar,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import {
  decodeReplayPayloads,
  latestSnapshot,
  replayInstant,
  sandboxSnapshotHTML,
  shortReplayID,
  type ReplaySnapshot,
} from './model';
import './styles.css';

const REPLAY_TYPES = ['session_replay_snapshot', 'session_replay_mutations'];
const PAGE_LIMIT = 20;
const MAX_PREVIEW_ITEMS = 600;

type PageProps = { project: Project; session: Session; environment: Environment };
type Preview = { snapshot: ReplaySnapshot; srcDoc: string; itemCount: number };

async function loadSnapshotPreview(
  projectID: string,
  environment: Environment,
  streamID: string,
  signal: AbortSignal,
): Promise<Preview> {
  const summaries: ContentItemSummary[] = [];
  let cursor: string | undefined;
  do {
    const page = await api.listContentItems(
      projectID,
      streamID,
      { environment, contentTypes: ['session_replay_snapshot'], cursor, limit: 100 },
      signal,
    );
    summaries.push(...page.items);
    if (summaries.length > MAX_PREVIEW_ITEMS)
      throw new Error(`该会话快照超过 ${MAX_PREVIEW_ITEMS} 个内容段，已拒绝在浏览器中展开。`);
    cursor = page.next_cursor;
  } while (cursor);
  if (summaries.length === 0) throw new Error('该会话还没有完整快照。');

  const items: ContentItem[] = [];
  for (let offset = 0; offset < summaries.length; offset += 8) {
    const batch = summaries.slice(offset, offset + 8);
    items.push(
      ...(await Promise.all(
        batch.map((item) => api.getContentItem(projectID, item.item_id, environment, signal)),
      )),
    );
  }
  const snapshot = latestSnapshot(await decodeReplayPayloads(items));
  if (!snapshot) throw new Error('没有可重建的完整 DOM 快照。');
  return { snapshot, srcDoc: sandboxSnapshotHTML(snapshot.html), itemCount: items.length };
}

export function SessionReplayPage(props: PageProps) {
  if (!props.project.permissions.includes('content.read'))
    return (
      <>
        <PageHeading title="会话回放" />
        <Alert tone="error" title="只有项目所有者或拥有 content.read 的机器可以读取受保护内容。" />
      </>
    );
  return <SessionReplayWorkspace key={`${props.project.id}:${props.environment}`} {...props} />;
}

function SessionReplayWorkspace({ project, environment }: PageProps) {
  const [streamCursors, setStreamCursors] = useState<string[]>([]);
  const [selectedStream, setSelectedStream] = useState<string>();
  const [itemCursors, setItemCursors] = useState<string[]>([]);
  const [preview, setPreview] = useState<Preview>();
  const [previewError, setPreviewError] = useState<Error>();
  const [previewLoading, setPreviewLoading] = useState(false);
  const previewController = useRef<AbortController | undefined>(undefined);

  const streams = useQuery({
    queryKey: ['content-streams', project.id, environment, streamCursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listContentStreams(
        project.id,
        {
          environment,
          contentTypes: REPLAY_TYPES,
          limit: PAGE_LIMIT,
          cursor: streamCursors.at(-1),
        },
        signal,
      ),
  });
  const items = useQuery({
    queryKey: [
      'content-items',
      project.id,
      environment,
      selectedStream ?? '',
      itemCursors.at(-1) ?? '',
    ],
    queryFn: ({ signal }) =>
      api.listContentItems(
        project.id,
        selectedStream!,
        {
          environment,
          contentTypes: REPLAY_TYPES,
          limit: PAGE_LIMIT,
          cursor: itemCursors.at(-1),
        },
        signal,
      ),
    enabled: !!selectedStream,
  });

  useEffect(
    () => () => {
      previewController.current?.abort();
    },
    [],
  );

  const selectStream = (streamID: string) => {
    previewController.current?.abort();
    setSelectedStream(streamID);
    setItemCursors([]);
    setPreview(undefined);
    setPreviewError(undefined);
    setPreviewLoading(false);
  };
  const openPreview = async () => {
    if (!selectedStream || previewLoading) return;
    previewController.current?.abort();
    const controller = new AbortController();
    previewController.current = controller;
    setPreviewLoading(true);
    setPreviewError(undefined);
    try {
      const loaded = await loadSnapshotPreview(
        project.id,
        environment,
        selectedStream,
        controller.signal,
      );
      if (!controller.signal.aborted) setPreview(loaded);
    } catch (error) {
      if (!controller.signal.aborted)
        setPreviewError(error instanceof Error ? error : new Error('无法加载回放快照。'));
    } finally {
      if (!controller.signal.aborted) setPreviewLoading(false);
    }
  };

  const streamColumns: DataTableColumn<ContentStreamSummary>[] = [
    {
      key: 'stream',
      header: '会话流',
      render: (row) => (
        <Button variant="link" className="project-link" onClick={() => selectStream(row.stream_id)}>
          <code title={row.stream_id}>{shortReplayID(row.stream_id)}</code>
        </Button>
      ),
    },
    { key: 'items', header: '完整内容项', render: (row) => row.item_count },
    {
      key: 'types',
      header: '内容类型',
      render: (row) => (
        <div className="replay-badges">
          {row.content_types.map((value) => (
            <Badge key={value}>
              {value === 'session_replay_snapshot' ? 'DOM 快照' : '增量记录'}
            </Badge>
          ))}
        </div>
      ),
    },
    { key: 'started', header: '开始时间', render: (row) => replayInstant(row.first_received_at) },
    { key: 'latest', header: '最近记录', render: (row) => replayInstant(row.last_received_at) },
  ];
  const itemColumns: DataTableColumn<ContentItemSummary>[] = [
    {
      key: 'item',
      header: '内容项',
      render: (row) => <code title={row.item_id}>{shortReplayID(row.item_id)}</code>,
    },
    {
      key: 'type',
      header: '类型',
      render: (row) => (
        <Badge>{row.content_type === 'session_replay_snapshot' ? 'DOM 快照' : '增量记录'}</Badge>
      ),
    },
    { key: 'chunks', header: '密文块', render: (row) => row.chunk_count },
    {
      key: 'bytes',
      header: '内容长度',
      render: (row) => `${row.content_length.toLocaleString()} B`,
    },
    { key: 'received', header: '接收时间', render: (row) => replayInstant(row.received_at) },
  ];

  return (
    <>
      <PageHeading title="会话回放" />
      <div className="inline-notice">
        <Alert
          tone="info"
          title="回放内容在浏览器端加密，只有项目所有者或 content.read 机器可读取。快照在禁脚本、禁网络的 sandbox 中预览。"
        />
      </div>
      <DataSurface label="回放会话流">
        {streams.isError ? (
          <Alert
            tone="error"
            title="无法读取回放会话"
            action={<Button onClick={() => void streams.refetch()}>重试</Button>}
          >
            {streams.error instanceof ApiError ? streams.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <>
            <DataTable
              caption="回放会话流列表"
              columns={streamColumns}
              rows={streams.data?.items ?? []}
              rowKey={(row) => row.stream_id}
              minWidth={820}
              loading={streams.isFetching}
              empty={<EmptyPanel description="当前环境还没有完整的会话回放内容。" />}
            />
            <PaginationBar
              page={streamCursors.length + 1}
              count={streams.data?.items.length ?? 0}
              hasNext={!!streams.data?.next_cursor}
              busy={streams.isFetching}
              onPrevious={() => setStreamCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (streams.data?.next_cursor)
                  setStreamCursors((current) => [...current, streams.data!.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>

      {selectedStream && (
        <DataSurface label="会话内容项">
          <Toolbar label="会话回放操作">
            <div className="replay-selection">
              <span>
                当前会话：<code title={selectedStream}>{shortReplayID(selectedStream)}</code>
              </span>
              <Button loading={previewLoading} onClick={() => void openPreview()}>
                加载安全快照预览
              </Button>
            </div>
          </Toolbar>
          {previewError && (
            <div className="inline-notice">
              <Alert tone="error" title="无法重建回放快照">
                {previewError.message}
              </Alert>
            </div>
          )}
          {items.isError ? (
            <Alert
              tone="error"
              title="无法读取会话内容项"
              action={<Button onClick={() => void items.refetch()}>重试</Button>}
            >
              {items.error instanceof ApiError ? items.error.message : '请稍后再试。'}
            </Alert>
          ) : (
            <>
              <DataTable
                caption="回放内容项列表"
                columns={itemColumns}
                rows={items.data?.items ?? []}
                rowKey={(row) => row.item_id}
                minWidth={760}
                loading={items.isFetching}
                empty={<EmptyPanel description="该会话还没有完整内容项。" />}
              />
              <PaginationBar
                page={itemCursors.length + 1}
                count={items.data?.items.length ?? 0}
                hasNext={!!items.data?.next_cursor}
                busy={items.isFetching}
                onPrevious={() => setItemCursors((current) => current.slice(0, -1))}
                onNext={() => {
                  if (items.data?.next_cursor)
                    setItemCursors((current) => [...current, items.data!.next_cursor!]);
                }}
              />
            </>
          )}
        </DataSurface>
      )}

      {preview && (
        <DataSurface label="安全快照预览">
          <div className="replay-preview-meta">
            <span>路由：{preview.snapshot.route || '—'}</span>
            <span>采集：{replayInstant(preview.snapshot.createdAt)}</span>
            <span>
              视口：{preview.snapshot.viewport.width} × {preview.snapshot.viewport.height}
            </span>
            <span>组装内容项：{preview.itemCount}</span>
          </div>
          <div className="replay-frame-shell">
            <iframe
              title="会话回放 DOM 快照"
              sandbox=""
              referrerPolicy="no-referrer"
              srcDoc={preview.srcDoc}
            />
          </div>
          <p className="evia-field-hint">
            当前仅展示最新完整 DOM 快照；增量 mutation 精确播放仍作为下一切片，不在此预览中推断。
          </p>
        </DataSurface>
      )}
    </>
  );
}
