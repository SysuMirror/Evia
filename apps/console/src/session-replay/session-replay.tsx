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
  LoadingPanel,
  Field,
  PageHeading,
  PaginationBar,
  ResultHeader,
  Select,
  type DataTableColumn,
} from '@evia/design-system';
import {
  applyReplayRecords,
  buildReplayChapters,
  decodeReplayPayloads,
  replayInstant,
  sandboxSnapshotHTML,
  shortReplayID,
  type ReplayChapter,
} from './model';
import './styles.css';

const REPLAY_TYPES = ['session_replay_snapshot', 'session_replay_mutations'];
const PAGE_LIMIT = 20;
const MAX_PREVIEW_ITEMS = 600;

type PageProps = { project: Project; session: Session; environment: Environment };
type Preview = { chapters: ReplayChapter[]; itemCount: number };

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
      { environment, contentTypes: REPLAY_TYPES, cursor, limit: 100 },
      signal,
    );
    summaries.push(...page.items);
    if (summaries.length > MAX_PREVIEW_ITEMS)
      throw new Error(`该会话回放超过 ${MAX_PREVIEW_ITEMS} 个内容段，已拒绝在浏览器中展开。`);
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
  const chapters = buildReplayChapters(await decodeReplayPayloads(items));
  if (chapters.length === 0) throw new Error('没有可重建的完整 DOM 快照。');
  return { chapters, itemCount: items.length };
}

export function SessionReplayPage(props: PageProps) {
  if (!props.project.permissions.includes('content.read'))
    return (
      <>
        <PageHeading
          title="会话回放"
          summary="浏览受保护的会话记录，并在隔离环境中检查安全快照。"
        />
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
  const [chapterIndex, setChapterIndex] = useState(0);
  const [playbackStep, setPlaybackStep] = useState(0);
  const [playing, setPlaying] = useState(false);
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const previewController = useRef<AbortController | undefined>(undefined);
  const activeChapter = preview?.chapters[chapterIndex];
  const activeTimeline = activeChapter?.timeline;

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
  useEffect(() => {
    if (!playing || !activeTimeline?.exact || playbackStep >= activeTimeline.records.length) return;
    const timer = window.setInterval(() => {
      setPlaybackStep((current) => Math.min(current + 1, activeTimeline.records.length));
    }, 250);
    return () => clearInterval(timer);
  }, [playing, playbackStep, activeTimeline]);
  useEffect(() => {
    if (activeTimeline && playbackStep >= activeTimeline.records.length) setPlaying(false);
  }, [playbackStep, activeTimeline]);

  const selectStream = (streamID: string) => {
    previewController.current?.abort();
    setSelectedStream(streamID);
    setItemCursors([]);
    setPreview(undefined);
    setPreviewError(undefined);
    setPreviewLoading(false);
    setChapterIndex(0);
    setPlaybackStep(0);
    setPlaying(false);
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
      if (!controller.signal.aborted) {
        setPreview(loaded);
        setChapterIndex(loaded.chapters.length - 1);
        setPlaybackStep(0);
        setPlaying(false);
      }
    } catch (error) {
      if (!controller.signal.aborted)
        setPreviewError(error instanceof Error ? error : new Error('无法加载回放快照。'));
    } finally {
      if (!controller.signal.aborted) setPreviewLoading(false);
    }
  };

  const selectedSummary = streams.data?.items.find((row) => row.stream_id === selectedStream);
  const playerState = previewError
    ? 'failed'
    : previewLoading
      ? 'loading'
      : activeTimeline?.exact
        ? 'exact'
        : activeTimeline
          ? 'snapshot'
          : selectedStream
            ? 'ready'
            : 'idle';
  const playerStateLabel = {
    idle: '待选择',
    ready: '待加载',
    loading: '组装中',
    exact: '精确回放',
    snapshot: '仅快照',
    failed: '失败',
  }[playerState];
  const playerStateTone =
    playerState === 'failed'
      ? 'error'
      : playerState === 'snapshot' || playerState === 'ready'
        ? 'warning'
        : playerState === 'exact'
          ? 'success'
          : 'neutral';
  const streamColumns: DataTableColumn<ContentStreamSummary>[] = [
    {
      key: 'stream',
      header: '会话',
      render: (row) => (
        <Button
          variant="link"
          className="project-link replay-stream-link"
          aria-pressed={selectedStream === row.stream_id}
          onClick={() => selectStream(row.stream_id)}
        >
          <code title={row.stream_id}>{shortReplayID(row.stream_id)}</code>
        </Button>
      ),
    },
    { key: 'items', header: '内容项', render: (row) => row.item_count },
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
      <PageHeading
        className="replay-page-heading"
        title="会话回放"
        summary="选择会话并在隔离播放器中检查净化后的记录。"
      />
      <div className="replay-workspace">
        <div className="replay-player">
          <DataSurface label="会话回放播放器">
            <ResultHeader
              title="安全播放器"
              summary={
                selectedStream ? (
                  <code title={selectedStream}>{shortReplayID(selectedStream)}</code>
                ) : (
                  '从会话列表选择一条记录'
                )
              }
              action={
                <>
                  <Badge tone={playerStateTone}>{playerStateLabel}</Badge>
                  {selectedStream && (playerState === 'ready' || playerState === 'failed') ? (
                    <Button loading={previewLoading} onClick={() => void openPreview()}>
                      {playerState === 'failed' ? '重新加载' : '加载回放'}
                    </Button>
                  ) : undefined}
                </>
              }
            />
            {playerState === 'failed' && previewError && (
              <div className="replay-failed-state">
                <Alert
                  tone="error"
                  title="无法重建当前回放"
                  action={<Button onClick={() => void openPreview()}>重新加载</Button>}
                >
                  {previewError.message} 请检查内容完整性，或从会话列表选择其他记录。
                </Alert>
              </div>
            )}
            {!selectedStream && <EmptyPanel description="选择左侧会话后可加载并播放安全快照。" />}
            {selectedStream && previewLoading && !preview && (
              <LoadingPanel label="正在组装安全回放" />
            )}
            {selectedStream && !previewLoading && !preview && !previewError && (
              <EmptyPanel description="内容记录已就绪，使用播放器标题栏的加载操作开始。" />
            )}
            {preview && activeChapter && activeTimeline && !previewError && (
              <>
                {preview.chapters.length > 1 && (
                  <div className="replay-chapter-selector">
                    <Field label="回放章节" htmlFor="replay-chapter">
                      <Select
                        id="replay-chapter"
                        value={String(chapterIndex)}
                        options={preview.chapters.map((chapter) => ({
                          value: String(chapter.index),
                          label: `第 ${chapter.index + 1} 章 · ${replayInstant(chapter.startedAt)}${chapter.timeline.exact ? '' : ' · 不完整'}`,
                        }))}
                        onChange={(event) => {
                          setPlaying(false);
                          setPlaybackStep(0);
                          setChapterIndex(Number(event.target.value));
                        }}
                      />
                    </Field>
                  </div>
                )}
                {activeChapter.resyncedFromIssue && (
                  <div className="replay-local-error">
                    <Alert tone="success" title="已从下一份完整快照重新同步">
                      前一章节因“{activeChapter.resyncedFromIssue}
                      ”停止精确播放；本章节从独立基线继续，未跨缺口猜测 DOM 状态。
                    </Alert>
                  </div>
                )}
                <div className="replay-preview-meta">
                  <span>路由：{activeTimeline.snapshot.route || '—'}</span>
                  <span>
                    视口：{activeTimeline.snapshot.viewport.width} ×{' '}
                    {activeTimeline.snapshot.viewport.height}
                  </span>
                  <span>
                    v{activeTimeline.snapshot.protocolVersion}，记录 {playbackStep}/
                    {activeTimeline.records.length}
                  </span>
                </div>
                {!activeTimeline.exact && (
                  <div className="replay-local-error">
                    <Alert tone="warning" title="仅显示安全快照，不提供播放控制">
                      {activeTimeline.issue} 记录不满足精确播放条件，可检查快照或选择其他会话。
                    </Alert>
                  </div>
                )}
                {activeTimeline.exact && (
                  <div className="replay-controls">
                    <Button onClick={() => setPlaying((current) => !current)}>
                      {playing ? '暂停' : '播放'}
                    </Button>
                    <Button
                      onClick={() => {
                        setPlaying(false);
                        setPlaybackStep(0);
                      }}
                    >
                      回到快照
                    </Button>
                    <input
                      aria-label="回放进度"
                      type="range"
                      min={0}
                      max={activeTimeline.records.length}
                      value={playbackStep}
                      onChange={(event) => {
                        setPlaying(false);
                        setPlaybackStep(Number(event.target.value));
                      }}
                    />
                  </div>
                )}
                <div className="replay-frame-shell">
                  <iframe
                    key={`${activeTimeline.snapshot.groupID}:${playbackStep}`}
                    ref={frameRef}
                    title="会话回放 DOM 快照"
                    sandbox="allow-same-origin"
                    referrerPolicy="no-referrer"
                    srcDoc={sandboxSnapshotHTML(activeTimeline.snapshot.html)}
                    onLoad={() => {
                      const document = frameRef.current?.contentDocument;
                      if (!document || playbackStep === 0) return;
                      try {
                        applyReplayRecords(document, activeTimeline.records.slice(0, playbackStep));
                      } catch (error) {
                        setPlaying(false);
                        setPreviewError(
                          error instanceof Error ? error : new Error('回放记录应用失败。'),
                        );
                      }
                    }}
                  />
                </div>
              </>
            )}
          </DataSurface>
        </div>

        <div className="replay-session-panel">
          <DataSurface label="回放会话流">
            <ResultHeader title="会话" summary="最近完整回放" />
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
                <div className="replay-session-scroll">
                  <DataTable
                    caption="回放会话流列表"
                    columns={streamColumns}
                    rows={streams.data?.items ?? []}
                    rowKey={(row) => row.stream_id}
                    minWidth={0}
                    loading={streams.isFetching}
                    empty={<EmptyPanel description="当前环境还没有完整的会话回放内容。" />}
                  />
                </div>
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
        </div>

        <details className="replay-diagnostics">
          <summary>
            <span>
              <strong>诊断与内容记录</strong>
              <span>传输内容项、安全边界与会话标识</span>
            </span>
            <span>展开详情</span>
          </summary>
          <div className="replay-diagnostics-grid">
            <div className="replay-records-panel">
              <DataSurface label="会话内容项">
                <ResultHeader
                  title="内容记录"
                  summary={selectedStream ? shortReplayID(selectedStream) : '选择会话后查看内容项'}
                />
                {!selectedStream ? (
                  <EmptyPanel description="尚未选择会话。" />
                ) : items.isError ? (
                  <Alert
                    tone="error"
                    title="无法读取会话内容项"
                    action={<Button onClick={() => void items.refetch()}>重试</Button>}
                  >
                    {items.error instanceof ApiError ? items.error.message : '请稍后再试。'}
                  </Alert>
                ) : (
                  <>
                    <div className="replay-records-scroll">
                      <DataTable
                        caption="回放内容项列表"
                        columns={itemColumns}
                        rows={items.data?.items ?? []}
                        rowKey={(row) => row.item_id}
                        minWidth={720}
                        loading={items.isFetching}
                        empty={<EmptyPanel description="该会话还没有完整内容项。" />}
                      />
                    </div>
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
            </div>

            <div className="replay-details-panel">
              <DataSurface label="回放详情">
                <ResultHeader title="安全与会话详情" />
                <div className="replay-details-content">
                  <Alert
                    tone="info"
                    title="回放内容在浏览器端解密，并在禁脚本、禁网络的 sandbox 中预览。"
                  />
                  {selectedSummary ? (
                    <dl className="detail-list">
                      <div>
                        <dt>会话标识</dt>
                        <dd>
                          <code title={selectedSummary.stream_id}>
                            {shortReplayID(selectedSummary.stream_id)}
                          </code>
                        </dd>
                      </div>
                      <div>
                        <dt>内容类型</dt>
                        <dd className="replay-badges">
                          {selectedSummary.content_types.map((value) => (
                            <Badge key={value}>
                              {value === 'session_replay_snapshot' ? 'DOM 快照' : '增量记录'}
                            </Badge>
                          ))}
                        </dd>
                      </div>
                      <div>
                        <dt>记录范围</dt>
                        <dd>
                          {replayInstant(selectedSummary.first_received_at)} 至{' '}
                          {replayInstant(selectedSummary.last_received_at)}
                        </dd>
                      </div>
                      {preview && activeTimeline && (
                        <div>
                          <dt>组装内容</dt>
                          <dd>
                            {preview.itemCount} 项，采集于{' '}
                            {replayInstant(activeTimeline.snapshot.createdAt)}
                          </dd>
                        </div>
                      )}
                    </dl>
                  ) : (
                    <p className="evia-field-hint">选择会话后显示记录范围与内容类型。</p>
                  )}
                  <p className="evia-field-hint">
                    v2
                    按完整快照拆分章节；遇到序号缺口或未知节点立即停止当前章节，并只从下一份完整快照重新同步。Shadow
                    DOM、iframe、canvas 与媒体状态不在当前捕获范围。
                  </p>
                </div>
              </DataSurface>
            </div>
          </div>
        </details>
      </div>
    </>
  );
}
