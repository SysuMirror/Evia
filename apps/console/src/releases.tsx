import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { TypeScriptArtifactPanel } from './artifacts/typescript-artifact-panel';
import {
  api,
  ApiError,
  type ActivationInput,
  type EventDefinition,
  type Release,
  type ReleaseInput,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  CopyButton,
  DataSurface,
  DataTable,
  EmptyPanel,
  LoadingPanel,
  Modal,
  PageHeading,
  Panel,
  PaginationBar,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { type CatalogPageProps } from './catalog';
import { CatalogError, environmentLabel, isCatalogAccessError } from './catalog-helpers';
import { useCatalogScope } from './catalog-scope';
import { useUnsavedWork } from './use-unsaved-work';

type PublishAttempt = { key: string; body: ReleaseInput };
type ActivateAttempt = { key: string; body: ActivationInput };
const stateLabels = {
  draft: '草稿',
  validating: '校验中',
  publishing: '发布中',
  published: '已发布',
  failed: '发布失败',
};
const uncertain = (error: Error | null) =>
  error instanceof ApiError && (error.status === 0 || error.status >= 500 || error.status === 408);
export function ReleasesPage({ project, session, environment }: CatalogPageProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const canWrite = project.permissions.includes('project.write');
  const [cursors, setCursors] = useState<string[]>([]);
  const [eventCursors, setEventCursors] = useState<string[]>([]);
  const [building, setBuilding] = useState(false);
  const [selected, setSelected] = useState<Record<string, number>>({});
  const [loadingDraft, setLoadingDraft] = useState<string>();
  const [selectionError, setSelectionError] = useState<Error | null>(null);
  const [publishAttempt, setPublishAttempt] = useState<PublishAttempt>();
  const [expected, setExpected] = useState<number>();
  const [detailID, setDetailID] = useState<string>();
  const [activationOpen, setActivationOpen] = useState(false);
  const [activationAttempt, setActivationAttempt] = useState<ActivateAttempt>();
  const [activationExpected, setActivationExpected] = useState<number>();
  const [pollCount, setPollCount] = useState(0);
  const [pollGeneration, setPollGeneration] = useState(0);
  const draftController = useRef<AbortController | null>(null);
  const cancelActivation = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    return () => {
      draftController.current?.abort();
    };
  }, []);
  const cursor = cursors.at(-1);
  const eventCursor = eventCursors.at(-1);
  const releases = useQuery({
    queryKey: ['releases', project.id, environment, cursor ?? ''],
    queryFn: ({ signal }) =>
      api.listReleases(project.id, { environment, cursor, limit: 20 }, signal),
  });
  const activation = useQuery({
    queryKey: ['activation', project.id, environment],
    queryFn: ({ signal }) => api.getActivation(project.id, environment, signal),
  });
  const events = useQuery({
    queryKey: ['release-events', project.id, environment, eventCursor ?? ''],
    queryFn: ({ signal }) =>
      api.listEventDefinitions(project.id, { environment, cursor: eventCursor, limit: 20 }, signal),
    enabled: building,
  });
  const detail = useQuery({
    queryKey: ['release-detail', project.id, detailID],
    queryFn: ({ signal }) => api.getRelease(project.id, detailID!, signal),
    enabled: !!detailID,
  });
  const releasesDenied = isCatalogAccessError(releases.error);
  const activationDenied = isCatalogAccessError(activation.error);
  const eventsDenied = isCatalogAccessError(events.error);
  const detailDenied = isCatalogAccessError(detail.error);
  const scope = useCatalogScope(
    JSON.stringify([project.id, environment, session.user_id, session.csrf_token]),
    !releasesDenied && !activationDenied && !eventsDenied && !detailDenied,
    { project, session },
  );
  const clearBuild = () => {
    setBuilding(false);
    setSelected({});
    setPublishAttempt(undefined);
    setExpected(undefined);
    setSelectionError(null);
  };
  const publish = useMutation({
    onMutate: scope.capture,
    mutationFn: (attempt: PublishAttempt) =>
      api.createRelease(project.id, attempt.body, attempt.key, session.csrf_token),
    onSuccess: async (release, _variables, token) => {
      if (!scope.isCurrent(token)) return;
      clearBuild();
      setDetailID(release.release_id);
      setPollCount(0);
      setPollGeneration((value) => value + 1);
      cache.setQueryData(['release-detail', project.id, release.release_id], release);
      setCursors([]);
      toast.success(`已发布不可变版本 v${release.revision}，请检查就绪状态后激活`);
      await cache.invalidateQueries({ queryKey: ['releases', project.id, environment] });
    },
  });
  const activate = useMutation({
    onMutate: scope.capture,
    mutationFn: (attempt: ActivateAttempt) =>
      api.activateRelease(project.id, attempt.body, attempt.key, session.csrf_token),
    onSuccess: async (result, _variables, token) => {
      if (!scope.isCurrent(token)) return;
      cache.setQueryData(['activation', project.id, environment], result);
      setActivationOpen(false);
      setActivationAttempt(undefined);
      toast.success(`已激活${environmentLabel[environment]} v${result.active_release_revision}`);
      await Promise.all([
        cache.invalidateQueries({ queryKey: ['releases', project.id, environment] }),
        detail.refetch(),
      ]);
    },
  });
  const busy = publish.isPending || activate.isPending || !!loadingDraft;
  const guard = useUnsavedWork({
    dirty: Object.keys(selected).length > 0 || !!publishAttempt || !!activationAttempt,
    busy,
    onDiscard: () => {
      setSelected({});
      setPublishAttempt(undefined);
      setActivationAttempt(undefined);
    },
  });
  const openDetails = (release: Release) => {
    setDetailID(release.release_id);
    setPollCount(0);
    setPollGeneration((value) => value + 1);
  };
  useEffect(() => {
    if (
      !detailID ||
      !detail.data ||
      detail.data.ingestion_ready ||
      detail.data.state === 'failed' ||
      detail.isError ||
      pollCount >= 20
    )
      return;
    const timer = window.setTimeout(() => {
      setPollCount((value) => value + 1);
      void detail.refetch();
    }, 3000);
    return () => window.clearTimeout(timer);
  }, [detailID, detail.data, detail.isError, detail.dataUpdatedAt, pollCount, pollGeneration]);
  const status = (release: Release) => (
    <div className="resource-statuses">
      <Badge
        tone={
          release.state === 'failed' ? 'error' : release.state === 'published' ? 'success' : 'info'
        }
      >
        {stateLabels[release.state]}
      </Badge>{' '}
      <Badge tone={release.ingestion_ready ? 'success' : 'warning'}>
        {release.ingestion_ready ? '管道就绪' : '尚未就绪'}
      </Badge>
      {!activationDenied && activation.data?.active_release_revision === release.revision && (
        <>
          {' '}
          <Badge tone="info">当前激活</Badge>
        </>
      )}
    </div>
  );
  const releaseColumns: DataTableColumn<Release>[] = [
    {
      key: 'revision',
      header: '发布版本',
      render: (release) => (
        <Button variant="link" onClick={() => openDetails(release)}>
          v{release.revision}
        </Button>
      ),
    },
    {
      key: 'environment',
      header: '环境',
      render: (release) => environmentLabel[release.environment],
    },
    { key: 'status', header: '状态', render: status },
    {
      key: 'action',
      header: '操作',
      kind: 'actions',
      render: (release) => (
        <Button size="small" onClick={() => openDetails(release)}>
          查看详情
        </Button>
      ),
    },
  ];
  const choose = async (event: EventDefinition, checked: boolean) => {
    if (!checked) {
      setSelected((current) => {
        const next = { ...current };
        delete next[event.event_key];
        return next;
      });
      return;
    }
    const token = scope.capture();
    const controller = new AbortController();
    draftController.current = controller;
    setLoadingDraft(event.event_key);
    setSelectionError(null);
    try {
      const draft = await api.getEventDraft(project.id, event.event_key, controller.signal);
      if (scope.isCurrent(token))
        setSelected((current) => ({ ...current, [event.event_key]: draft.revision }));
    } catch (error) {
      if (scope.isCurrent(token) && !controller.signal.aborted) setSelectionError(error as Error);
    } finally {
      if (scope.isCurrent(token)) setLoadingDraft(undefined);
    }
  };
  const eventColumns: DataTableColumn<EventDefinition>[] = [
    {
      key: 'select',
      header: '选择',
      width: 80,
      render: (event) => (
        <Checkbox
          aria-label={`选择事件 ${event.display_name}`}
          checked={selected[event.event_key] !== undefined}
          disabled={
            !canWrite ||
            busy ||
            !!publishAttempt ||
            (selected[event.event_key] === undefined && Object.keys(selected).length >= 200)
          }
          onChange={(input) => void choose(event, input.target.checked)}
        />
      ),
    },
    {
      key: 'name',
      header: '事件',
      render: (event) => (
        <>
          {event.display_name}
          <br />
          <code>{event.event_key}</code>
        </>
      ),
    },
    {
      key: 'revision',
      header: '选定草稿',
      render: (event) =>
        loadingDraft === event.event_key
          ? '正在读取'
          : selected[event.event_key] !== undefined
            ? `r${selected[event.event_key]}`
            : '未选择',
    },
  ];
  const activeRevision = activationDenied ? undefined : activation.data?.active_release_revision;
  return (
    <>
      <PageHeading
        title="发布管理"
        action={
          <>
            <Button
              loading={releases.isFetching || activation.isFetching}
              onClick={() => {
                void releases.refetch();
                void activation.refetch();
              }}
            >
              刷新
            </Button>
            <Button
              variant="primary"
              disabled={!canWrite || releasesDenied || activation.isPending || activation.isError}
              onClick={() => {
                publish.reset();
                setBuilding(true);
                setExpected(activeRevision);
              }}
            >
              创建发布
            </Button>
          </>
        }
      />
      <div className="resource-summary">
        <div>
          <div className="resource-muted">{environmentLabel[environment]}，当前激活版本</div>
          <div className="resource-summary-title">
            {activation.isError
              ? '读取失败'
              : activeRevision === undefined
                ? '正在读取'
                : activeRevision === 0
                  ? '尚未激活版本'
                  : `v${activeRevision}`}
          </div>
        </div>
        <span className="evia-field-hint">发布后先检查管道就绪状态，再激活版本以接收事件。</span>
      </div>
      {!canWrite && <p>当前权限为只读，可查看发布及激活状态。</p>}
      <CatalogError error={activation.error} retry={() => void activation.refetch()} />
      <CatalogError
        error={releases.error}
        retry={() => {
          if (releases.error instanceof ApiError && releases.error.code === 'INVALID_CURSOR')
            setCursors([]);
          else void releases.refetch();
        }}
      />
      {releases.isPending ? (
        <LoadingPanel label="正在加载发布" />
      ) : (
        !releasesDenied &&
        releases.data && (
          <DataSurface
            label="发布列表"
            footer={
              <PaginationBar
                page={cursors.length + 1}
                count={releases.data.items.length}
                hasNext={!!releases.data.next_cursor}
                busy={releases.isFetching}
                onPrevious={() => setCursors((values) => values.slice(0, -1))}
                onNext={() => {
                  if (releases.data.next_cursor)
                    setCursors((values) => [...values, releases.data.next_cursor!]);
                }}
              />
            }
          >
            <DataTable
              caption="发布列表"
              columns={releaseColumns}
              rows={releases.data.items}
              rowKey={(release) => release.release_id}
              loading={releases.isFetching}
              minWidth={680}
              empty={
                <EmptyPanel description="尚无发布，选择已保存的事件草稿创建首个不可变版本。" />
              }
            />
          </DataSurface>
        )
      )}
      {!releasesDenied && !activationDenied && (
        <Modal
          title="创建不可变发布"
          open={building && !releasesDenied && !activationDenied}
          busy={busy}
          onRequestClose={() => guard.requestLeave(clearBuild)}
          footer={
            <>
              <Button disabled={busy} onClick={() => guard.requestLeave(clearBuild)}>
                取消
              </Button>
              <Button
                variant="primary"
                loading={publish.isPending}
                disabled={
                  !canWrite || busy || !Object.keys(selected).length || expected === undefined
                }
                onClick={() => {
                  if (busy || !canWrite || expected === undefined) return;
                  const attempt = publishAttempt ?? {
                    key: crypto.randomUUID(),
                    body: {
                      environment,
                      expected_active_revision: expected,
                      events: Object.entries(selected)
                        .sort(([a], [b]) => a.localeCompare(b))
                        .map(([event_key, draft_revision]) => ({ event_key, draft_revision })),
                    },
                  };
                  setPublishAttempt(attempt);
                  publish.mutate(attempt);
                }}
              >
                {publishAttempt ? '重试同一发布请求' : '发布所选草稿'}
              </Button>
            </>
          }
        >
          {!eventsDenied && (
            <div className="resource-summary">
              <p>
                {environmentLabel[environment]}，比较当前激活版本{' '}
                {expected === 0 ? '0（未激活）' : `v${expected ?? '—'}`}。包含{' '}
                {Object.keys(selected).length} 个事件，发布内容以选定的精确草稿版本为准，最多选择
                200 个事件。
              </p>
            </div>
          )}
          <CatalogError
            error={publish.error ?? selectionError ?? events.error}
            retry={
              events.isError
                ? () => {
                    if (events.error instanceof ApiError && events.error.code === 'INVALID_CURSOR')
                      setEventCursors([]);
                    else void events.refetch();
                  }
                : undefined
            }
          />
          {publishAttempt && (
            <Alert
              tone="warning"
              title={uncertain(publish.error) ? '尚未确认发布结果' : '本次请求内容已固定'}
            >
              重试将复用同一幂等键和事件版本，避免重复创建。
              {publish.error && !uncertain(publish.error) && (
                <Button
                  size="small"
                  disabled={busy || activation.isFetching}
                  onClick={async () => {
                    const token = scope.capture();
                    const result = await activation.refetch();
                    if (
                      scope.isCurrent(token) &&
                      !isCatalogAccessError(result.error) &&
                      result.data
                    ) {
                      setExpected(result.data.active_release_revision);
                      setPublishAttempt(undefined);
                      publish.reset();
                    }
                  }}
                >
                  读取当前激活版本，重新配置发布
                </Button>
              )}
            </Alert>
          )}
          {!eventsDenied && Object.keys(selected).length > 0 && (
            <details open className="resource-selection">
              <summary>已选草稿（{Object.keys(selected).length}）</summary>
              <ul>
                {Object.entries(selected).map(([key, revision]) => (
                  <li key={key}>
                    <code>{key}</code>，r{revision}
                  </li>
                ))}
              </ul>
            </details>
          )}
          {events.isPending ? (
            <LoadingPanel label="正在加载可发布事件" />
          ) : (
            !eventsDenied &&
            events.data && (
              <DataSurface
                label="选择发布草稿"
                footer={
                  <PaginationBar
                    page={eventCursors.length + 1}
                    count={events.data.items.length}
                    hasNext={!!events.data.next_cursor}
                    busy={events.isFetching || busy || !!publishAttempt}
                    onPrevious={() => setEventCursors((values) => values.slice(0, -1))}
                    onNext={() => {
                      if (events.data.next_cursor)
                        setEventCursors((values) => [...values, events.data.next_cursor!]);
                    }}
                  />
                }
              >
                <DataTable
                  caption="选择发布草稿"
                  columns={eventColumns}
                  rows={events.data.items}
                  rowKey={(event) => event.event_key}
                  minWidth={420}
                  loading={events.isFetching}
                  empty={<EmptyPanel description="尚无可选事件，请先创建并保存事件草稿。" />}
                />
              </DataSurface>
            )
          )}
          <p>发布会验证触发条件、事实类型、来源和已保存样例。未保存的编辑不包含在本次发布中。</p>
        </Modal>
      )}
      {!releasesDenied && (
        <Modal
          title="发布详情"
          open={!!detailID}
          busy={activate.isPending}
          onRequestClose={() => {
            if (!activationOpen) setDetailID(undefined);
          }}
          footer={
            <>
              <Button disabled={activate.isPending} onClick={() => setDetailID(undefined)}>
                关闭
              </Button>
              <Button
                loading={detail.isFetching}
                disabled={activate.isPending}
                onClick={() => {
                  setPollCount(0);
                  setPollGeneration((value) => value + 1);
                  void detail.refetch();
                  void activation.refetch();
                }}
              >
                刷新状态
              </Button>
              <Button
                variant="primary"
                disabled={
                  !canWrite ||
                  detailDenied ||
                  !detail.data ||
                  !detail.data.ingestion_ready ||
                  detail.data.state !== 'published' ||
                  activeRevision === undefined ||
                  detail.data.revision <= activeRevision ||
                  activation.isFetching ||
                  activation.isError
                }
                onClick={() => {
                  activate.reset();
                  setActivationAttempt(undefined);
                  setActivationExpected(activeRevision);
                  setActivationOpen(true);
                }}
              >
                激活此版本
              </Button>
            </>
          }
        >
          <CatalogError error={detail.error} retry={() => void detail.refetch()} />
          {detail.isPending ? (
            <LoadingPanel label="正在加载发布详情" />
          ) : (
            !detailDenied &&
            detail.data && (
              <>
                <div className="resource-summary">
                  <div className="resource-summary-title">
                    v{detail.data.revision}{' '}
                    <span className="resource-muted">
                      {environmentLabel[detail.data.environment]}
                    </span>
                  </div>
                  {status(detail.data)}
                </div>
                <Panel title="发布记录" className="resource-section">
                  <dl className="project-details">
                    <div>
                      <dt>发布 ID</dt>
                      <dd>
                        <code>{detail.data.release_id}</code>{' '}
                        <CopyButton value={detail.data.release_id} />
                      </dd>
                    </div>
                    <div>
                      <dt>清单摘要</dt>
                      <dd className="resource-origin-list">
                        {detail.data.manifest_hash ?? '尚无摘要'}
                      </dd>
                    </div>
                    <div>
                      <dt>加速状态</dt>
                      <dd>
                        {
                          {
                            not_requested: '未请求',
                            building: '构建中',
                            ready: '已就绪',
                            failed: '失败',
                          }[detail.data.acceleration_state]
                        }
                      </dd>
                    </div>
                  </dl>
                </Panel>
                <TypeScriptArtifactPanel project={project} releaseID={detail.data.release_id} />
                {detail.data.error && (
                  <Alert tone="error" title={detail.data.error.message}>
                    {detail.data.error.code}
                  </Alert>
                )}
                {activeRevision !== undefined && detail.data.revision <= activeRevision && (
                  <p className="evia-field-hint">
                    {detail.data.revision === activeRevision
                      ? '此版本已激活。'
                      : `当前已激活 v${activeRevision}，只可激活更新版本。`}
                  </p>
                )}
                {!detail.data.ingestion_ready && (
                  <Alert
                    tone="warning"
                    title={
                      pollCount >= 20
                        ? '已停止自动检查，请刷新状态'
                        : detail.isError
                          ? '检查失败，请重试'
                          : '尚未获得有效的管道就绪租约'
                    }
                  >
                    管道验证通过后才可激活。等待交付服务完成验证，也可手动刷新状态。
                  </Alert>
                )}
              </>
            )
          )}
        </Modal>
      )}
      {!releasesDenied && !detailDenied && !activationDenied && (
        <Modal
          title="确认激活发布"
          open={activationOpen && !detailDenied && !activationDenied}
          busy={activate.isPending}
          initialFocusRef={cancelActivation}
          onRequestClose={() =>
            guard.requestLeave(() => {
              setActivationOpen(false);
              setActivationAttempt(undefined);
            })
          }
          footer={
            <>
              <Button
                ref={cancelActivation}
                disabled={activate.isPending}
                onClick={() =>
                  guard.requestLeave(() => {
                    setActivationOpen(false);
                    setActivationAttempt(undefined);
                  })
                }
              >
                暂不激活
              </Button>
              <Button
                variant="primary"
                loading={activate.isPending}
                disabled={
                  !canWrite ||
                  detailDenied ||
                  !detail.data ||
                  activationExpected === undefined ||
                  (!activationAttempt && activationExpected >= detail.data.revision)
                }
                onClick={() => {
                  if (
                    !detail.data ||
                    activationExpected === undefined ||
                    activate.isPending ||
                    !canWrite
                  )
                    return;
                  const attempt = activationAttempt ?? {
                    key: crypto.randomUUID(),
                    body: {
                      environment,
                      release_id: detail.data.release_id,
                      expected_active_revision: activationExpected,
                    },
                  };
                  setActivationAttempt(attempt);
                  activate.mutate(attempt);
                }}
              >
                {activationAttempt ? '重试同一激活请求' : '确认激活'}
              </Button>
            </>
          }
        >
          <p>
            将项目「{project.name}」的{environmentLabel[environment]}从{' '}
            {activationExpected === 0 ? '未激活' : `v${activationExpected ?? '—'}`} 切换到 v
            {detail.data?.revision}。服务端将重新验证管道及当前激活版本。激活版本只会递增。
          </p>
          <CatalogError error={activate.error} />
          {activationAttempt && (
            <Alert
              tone="warning"
              title={
                uncertain(activate.error)
                  ? '激活结果尚未确认，重试会复用原幂等键'
                  : '本次激活请求已固定'
              }
            >
              {activate.error && !uncertain(activate.error) && (
                <Button
                  disabled={activate.isPending || activation.isFetching}
                  onClick={async () => {
                    const token = scope.capture();
                    const result = await activation.refetch();
                    if (
                      scope.isCurrent(token) &&
                      !isCatalogAccessError(result.error) &&
                      result.data
                    ) {
                      setActivationExpected(result.data.active_release_revision);
                      setActivationAttempt(undefined);
                      activate.reset();
                    }
                  }}
                >
                  读取最新激活版本后重新确认
                </Button>
              )}
            </Alert>
          )}
        </Modal>
      )}
      {guard.confirmation}
    </>
  );
}
