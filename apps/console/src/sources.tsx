import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type Session,
  type Source,
  type SourceInput,
  type SourceIssued,
  type SourceUpdate,
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
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Select,
  Textarea,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { useUnsavedWork } from './use-unsaved-work';
import { parseSourceOrigins } from './source-origins';

const environments: Record<Environment, string> = {
  development: '开发',
  staging: '预发布',
  production: '生产',
};
const applicationLabels = {
  pending: '等待缓存失效',
  applied: '缓存失效窗口已过',
  failed: '应用失败',
};
const applicationHelp =
  '配置已保存后，接收器首次观察到版本才开始等待最长 2 秒的缓存窗口。窗口已过不表示每个节点逐一确认；依赖不可用时接入关闭。';

function Failure({ error, retry }: { error: Error; retry?: () => void }) {
  return (
    <div className="inline-notice">
      <ErrorPanel
        message={error.message}
        requestID={error instanceof ApiError ? error.requestID : undefined}
        retry={retry}
      />
    </div>
  );
}
function ApplicationState({ source }: { source: Source }) {
  return (
    <Badge
      tone={
        source.application_state === 'applied'
          ? 'success'
          : source.application_state === 'failed'
            ? 'error'
            : 'neutral'
      }
    >
      {applicationLabels[source.application_state]}
    </Badge>
  );
}
function safeSource(issued: SourceIssued): Source {
  // Only this explicit public projection may enter server-state caches.
  return {
    id: issued.id,
    name: issued.name,
    environment: issued.environment,
    kind: issued.kind,
    write_key: issued.write_key,
    status: issued.status,
    revision: issued.revision,
    application_state: issued.application_state,
    allowed_origins: issued.allowed_origins,
  };
}
function uncertain(error: Error | undefined) {
  return !!error && (!(error instanceof ApiError) || error.status === 0 || error.status >= 500);
}
function asError(error: unknown) {
  return error instanceof Error ? error : new Error('请求失败，请稍后重试');
}

export function SourcesPage(props: {
  project: Project;
  session: Session;
  environment: Environment;
}) {
  return (
    <SourcesWorkspace
      key={`${props.project.id}:${props.environment}:${props.session.user_id}`}
      {...props}
    />
  );
}

function SourcesWorkspace({
  project,
  session,
  environment,
}: {
  project: Project;
  session: Session;
  environment: Environment;
}) {
  const cache = useQueryClient();
  const toast = useToast();
  const canWrite = project.permissions.includes('project.write');
  const [cursors, setCursors] = useState<string[]>([]);
  const [selectedID, setSelectedID] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [kind, setKind] = useState<SourceInput['kind']>('browser');
  const [origins, setOrigins] = useState('');
  const [createErrors, setCreateErrors] = useState<{ name?: string; origins?: string }>({});
  const [createError, setCreateError] = useState<Error>();
  const [editBase, setEditBase] = useState<Source>();
  const [editOrigins, setEditOrigins] = useState('');
  const [editError, setEditError] = useState<Error>();
  const [originError, setOriginError] = useState<string>();
  const [statusTarget, setStatusTarget] = useState<Source>();
  const [statusError, setStatusError] = useState<Error>();
  const [rotationTarget, setRotationTarget] = useState<Source>();
  const [overlap, setOverlap] = useState('3600');
  const [rotationAttempt, setRotationAttempt] = useState<{ key: string; seconds: number }>();
  const [rotationError, setRotationError] = useState<Error>();
  const [overlapError, setOverlapError] = useState<string>();
  const [issued, setIssued] = useState<{ source: Source; secret: string }>();
  const [secretVisible, setSecretVisible] = useState(false);
  const [secretSaved, setSecretSaved] = useState(false);
  const [busy, setBusy] = useState<'create' | 'edit' | 'status' | 'rotation'>();
  const [pollRound, setPollRound] = useState(0);
  const [pollVersion, setPollVersion] = useState(0);
  const busyRef = useRef(false);
  const generation = useRef(0);
  const nameRef = useRef<HTMLInputElement>(null);
  const originRef = useRef<HTMLTextAreaElement>(null);
  const editRef = useRef<HTMLTextAreaElement>(null);
  const overlapRef = useRef<HTMLInputElement>(null);
  const statusCancelRef = useRef<HTMLButtonElement>(null);
  const rotationCancelRef = useRef<HTMLButtonElement>(null);
  const editing = !!editBase;
  useEffect(() => {
    if (!editing) return;
    const frame = requestAnimationFrame(() => editRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [editing]);
  useEffect(() => {
    generation.current += 1;
    return () => {
      generation.current += 1;
    };
  }, []);

  const listKey = ['sources', project.id, environment] as const;
  const sourceKey = (id: string) => ['source', project.id, environment, id] as const;
  const list = useQuery({
    queryKey: [...listKey, cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listSources(project.id, { environment, limit: 20, cursor: cursors.at(-1) }, signal),
  });
  const detail = useQuery({
    queryKey: sourceKey(selectedID ?? ''),
    queryFn: ({ signal }) => api.getSource(project.id, selectedID!, signal),
    enabled: !!selectedID,
    staleTime: 0,
  });
  const source = detail.data?.environment === environment ? detail.data : undefined;
  const inaccessible = (error: Error | null) =>
    error instanceof ApiError && [401, 403, 404].includes(error.status);
  const pending =
    !!list.data?.items.some((item) => item.application_state === 'pending') ||
    source?.application_state === 'pending';
  useEffect(() => {
    if (!pending || pollRound >= 10 || inaccessible(list.error) || inaccessible(detail.error))
      return;
    const timer = window.setTimeout(() => {
      setPollRound((round) => round + 1);
      void list.refetch();
      if (selectedID) void detail.refetch();
    }, 2000);
    return () => window.clearTimeout(timer);
  }, [
    pending,
    pollRound,
    pollVersion,
    selectedID,
    list.refetch,
    detail.refetch,
    list.error,
    detail.error,
  ]);
  const restartPolling = () => {
    setPollRound(0);
    setPollVersion((version) => version + 1);
  };
  const refresh = () => {
    restartPolling();
    void list.refetch();
    if (selectedID) void detail.refetch();
  };

  const createDirty = creating && (name !== '' || origins !== '' || kind !== 'browser');
  const editDirty = !!editBase && editOrigins !== editBase.allowed_origins.join('\n');
  const rotationDirty = !!rotationTarget && (overlap !== '3600' || !!rotationAttempt);
  const clearLocal = () => {
    setCreating(false);
    setName('');
    setKind('browser');
    setOrigins('');
    setCreateErrors({});
    setCreateError(undefined);
    setEditBase(undefined);
    setEditOrigins('');
    setEditError(undefined);
    setOriginError(undefined);
    setStatusTarget(undefined);
    setStatusError(undefined);
    setRotationTarget(undefined);
    setRotationAttempt(undefined);
    setRotationError(undefined);
    setOverlapError(undefined);
    setOverlap('3600');
    setIssued(undefined);
    setSecretVisible(false);
    setSecretSaved(false);
  };
  const guard = useUnsavedWork({
    dirty: createDirty || editDirty || rotationDirty || (!!issued && !secretSaved),
    busy: !!busy,
    onDiscard: clearLocal,
  });
  const closeCreate = () =>
    guard.requestLeave(() => {
      setCreating(false);
      setName('');
      setOrigins('');
      setKind('browser');
      setCreateError(undefined);
      setCreateErrors({});
    });
  const closeDetail = () =>
    guard.requestLeave(() => {
      setSelectedID(undefined);
      setEditBase(undefined);
      setEditOrigins('');
      setEditError(undefined);
      setOriginError(undefined);
    });
  const closeRotation = () =>
    guard.requestLeave(() => {
      setRotationTarget(undefined);
      setRotationAttempt(undefined);
      setRotationError(undefined);
      setOverlap('3600');
    });
  const closeIssued = () =>
    guard.requestLeave(() => {
      setIssued(undefined);
      setSecretVisible(false);
      setSecretSaved(false);
    });
  async function perform(
    operation: NonNullable<typeof busy>,
    task: (isCurrent: () => boolean) => Promise<void>,
    failed: (error: Error) => void,
  ) {
    if (busyRef.current || !canWrite) return;
    busyRef.current = true;
    setBusy(operation);
    const version = generation.current;
    const isCurrent = () => generation.current === version;
    try {
      await task(isCurrent);
    } catch (error) {
      if (isCurrent()) {
        if (error instanceof ApiError && error.status === 401) {
          clearLocal();
          cache.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
          cache.setQueryData(['session'], null);
        } else failed(asError(error));
      }
    } finally {
      if (isCurrent()) {
        busyRef.current = false;
        setBusy(undefined);
      }
    }
  }
  function saved(result: Source) {
    cache.setQueryData(sourceKey(result.id), result);
    void cache.invalidateQueries({ queryKey: listKey });
    restartPolling();
  }
  function receiveCredentials(result: SourceIssued) {
    const clean = safeSource(result);
    saved(clean);
    if (clean.kind === 'server' && result.server_secret_once) {
      setSecretVisible(false);
      setSecretSaved(false);
      setIssued({ source: clean, secret: result.server_secret_once });
    } else if (clean.kind === 'server') {
      // A malformed response must never pretend that credentials were received.
      toast.error('凭据已提交，但未收到一次性密钥，请重新轮换后保存');
    }
    return clean;
  }
  const conflict = editError instanceof ApiError && editError.code === 'SOURCE_REVISION_CONFLICT';
  const columns: DataTableColumn<Source>[] = [
    {
      key: 'name',
      header: '来源',
      render: (item) => (
        <Button
          variant="link"
          className="project-link"
          onClick={() => {
            setSelectedID(item.id);
            setEditBase(undefined);
            setEditError(undefined);
            restartPolling();
          }}
        >
          <span className="project-name">{item.name}</span>
        </Button>
      ),
    },
    {
      key: 'kind',
      header: '类型',
      width: 110,
      render: (item) => (item.kind === 'browser' ? '浏览器' : '服务端'),
    },
    {
      key: 'status',
      header: '接入状态',
      width: 110,
      render: (item) => (
        <Badge tone={item.status === 'active' ? 'success' : 'neutral'}>
          {item.status === 'active' ? '启用' : '停用'}
        </Badge>
      ),
    },
    {
      key: 'application',
      header: '配置应用',
      width: 180,
      render: (item) => <ApplicationState source={item} />,
    },
    {
      key: 'origins',
      header: '允许的 Origin',
      render: (item) =>
        item.kind === 'server' ? (
          '不适用'
        ) : (
          <span style={{ overflowWrap: 'anywhere' }}>{item.allowed_origins.join('，')}</span>
        ),
    },
  ];
  return (
    <>
      <PageHeading
        title="来源管理"
        action={
          <>
            <Button onClick={refresh} loading={list.isFetching} disabled={!!busy}>
              刷新
            </Button>
            {canWrite && (
              <Button
                variant="primary"
                onClick={() => {
                  clearLocal();
                  setCreating(true);
                }}
              >
                创建来源
              </Button>
            )}
          </>
        }
      />
      {!canWrite && <Alert tone="info" title="当前项目为只读权限，可查看来源配置。" />}
      {list.isError && (
        <Failure
          error={list.error}
          retry={() => {
            if (list.error instanceof ApiError && list.error.code === 'INVALID_CURSOR')
              setCursors([]);
            else refresh();
          }}
        />
      )}
      {list.isPending ? (
        <LoadingPanel label="正在加载来源" />
      ) : list.data && !inaccessible(list.error) ? (
        <DataSurface
          label="来源列表"
          footer={
            <PaginationBar
              page={cursors.length + 1}
              count={list.data.items.length}
              hasNext={!!list.data.next_cursor}
              busy={list.isFetching}
              onPrevious={() => {
                setCursors((current) => current.slice(0, -1));
                restartPolling();
              }}
              onNext={() => {
                if (list.data.next_cursor)
                  setCursors((current) => [...current, list.data.next_cursor!]);
                restartPolling();
              }}
            />
          }
        >
          <DataTable
            caption="来源列表"
            columns={columns}
            rows={list.data.items}
            rowKey={(item) => item.id}
            minWidth={740}
            loading={list.isFetching}
            empty={
              <EmptyPanel
                description={
                  cursors.length ? '这一页没有更多来源' : `${environments[environment]}环境暂无来源`
                }
              />
            }
          />
        </DataSurface>
      ) : null}
      {pending && (
        <p className="evia-field-hint" role="status">
          {pollRound >= 10
            ? '自动检查已暂停，接收器可能尚未观察到配置。可手动刷新继续检查。'
            : '正在检查配置缓存状态。'}
        </p>
      )}

      <Modal
        title="创建来源"
        open={creating}
        onRequestClose={closeCreate}
        busy={busy === 'create'}
        initialFocusRef={nameRef}
        footer={
          <>
            <Button onClick={closeCreate} disabled={!!busy}>
              取消
            </Button>
            <Button
              variant="primary"
              type="submit"
              form="source-create"
              loading={busy === 'create'}
              disabled={!canWrite || uncertain(createError)}
            >
              创建
            </Button>
          </>
        }
      >
        {createError && <Failure error={createError} />}
        {uncertain(createError) && (
          <Alert tone="warning" title="请先核对创建结果">
            创建结果尚不确定，重复创建可能产生多个来源。请关闭后刷新列表核对；若服务端密钥未收到，可在该来源中轮换凭据。
          </Alert>
        )}
        <form
          id="source-create"
          className="console-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (busyRef.current || uncertain(createError)) return;
            const parsed = kind === 'browser' ? parseSourceOrigins(origins) : { origins: [] };
            const errors = {
              name: !name.trim()
                ? '请输入来源名称'
                : [...name.trim()].length > 100 || name.includes('\0')
                  ? '来源名称必须为 1 到 100 个字符'
                  : undefined,
              origins: parsed.error,
            };
            setCreateErrors(errors);
            if (errors.name) {
              nameRef.current?.focus();
              return;
            }
            if (errors.origins) {
              originRef.current?.focus();
              return;
            }
            setCreateError(undefined);
            const body: SourceInput = {
              name: name.trim(),
              environment,
              kind,
              allowed_origins: parsed.origins,
            };
            void perform(
              'create',
              async (isCurrent) => {
                const result = await api.createSource(project.id, body, session.csrf_token);
                if (!isCurrent()) return;
                const clean = receiveCredentials(result);
                setCreating(false);
                setName('');
                setOrigins('');
                setKind('browser');
                setCursors([]);
                setSelectedID(clean.id);
                toast.success(`已创建来源「${clean.name}」`);
              },
              setCreateError,
            );
          }}
        >
          <Field label="来源名称" htmlFor="source-name" required error={createErrors.name}>
            <Input
              ref={nameRef}
              id="source-name"
              autoComplete="off"
              required
              maxLength={100}
              disabled={!!busy || uncertain(createError)}
              value={name}
              onChange={(event) => {
                setName(event.target.value);
                event.target.setCustomValidity(event.target.value.trim() ? '' : '请输入来源名称');
                setCreateErrors((current) => ({ ...current, name: undefined }));
              }}
              onInvalid={() =>
                setCreateErrors((current) => ({
                  ...current,
                  name: '请输入 1 到 100 个字符的来源名称',
                }))
              }
            />
          </Field>
          <Field label="环境" htmlFor="source-environment">
            <Input id="source-environment" value={environments[environment]} readOnly />
          </Field>
          <Field label="来源类型" htmlFor="source-kind" required>
            <Select
              id="source-kind"
              value={kind}
              disabled={!!busy || uncertain(createError)}
              options={[
                { value: 'browser', label: '浏览器' },
                { value: 'server', label: '服务端' },
              ]}
              onChange={(event) => {
                setKind(event.target.value as SourceInput['kind']);
                setCreateErrors((current) => ({ ...current, origins: undefined }));
              }}
            />
          </Field>
          {kind === 'browser' ? (
            <Field
              label="允许的 Origin"
              htmlFor="source-origins"
              required
              error={createErrors.origins}
              hint="每行一个完整 Origin，例如 https://app.example.com 或 http://localhost:3000。不含末尾斜线、路径、查询、通配符或凭据，最多 50 个。"
            >
              <Textarea
                ref={originRef}
                id="source-origins"
                rows={5}
                required
                value={origins}
                disabled={!!busy || uncertain(createError)}
                spellCheck={false}
                autoComplete="off"
                onChange={(event) => {
                  setOrigins(event.target.value);
                  setCreateErrors((current) => ({ ...current, origins: undefined }));
                }}
                onInvalid={() =>
                  setCreateErrors((current) => ({ ...current, origins: '至少填写一个 Origin' }))
                }
              />
            </Field>
          ) : (
            <Alert tone="info" title="妥善保存服务端密钥">
              服务端来源使用写入键和 Bearer
              密钥。密钥只在创建或轮换成功时提供，请保存在服务端，避免放入浏览器。
            </Alert>
          )}
        </form>
      </Modal>

      <Modal
        title={source ? `来源详情：${source.name}` : '来源详情'}
        open={!!selectedID}
        onRequestClose={closeDetail}
        busy={!!busy}
        initialFocusRef={editBase ? editRef : undefined}
        footer={
          <Button onClick={closeDetail} disabled={!!busy}>
            完成
          </Button>
        }
      >
        {detail.isError && (
          <Failure
            error={detail.error}
            retry={() => {
              restartPolling();
              void detail.refetch();
            }}
          />
        )}
        {detail.isPending ? (
          <LoadingPanel label="正在加载来源详情" />
        ) : source && !inaccessible(detail.error) ? (
          <>
            <dl className="project-details">
              <div>
                <dt>来源名称</dt>
                <dd>{source.name}</dd>
              </div>
              <div>
                <dt>来源 ID</dt>
                <dd>
                  <code>{source.id}</code> <CopyButton value={source.id} />
                </dd>
              </div>
              <div>
                <dt>环境和类型</dt>
                <dd>
                  {environments[source.environment]}，
                  {source.kind === 'browser' ? '浏览器' : '服务端'}
                </dd>
              </div>
              <div>
                <dt>写入键</dt>
                <dd>
                  <code>{source.write_key}</code>{' '}
                  <CopyButton value={source.write_key} label="复制写入键" />
                </dd>
              </div>
              <div>
                <dt>接入状态</dt>
                <dd>{source.status === 'active' ? '启用' : '停用'}</dd>
              </div>
              <div>
                <dt>配置应用</dt>
                <dd>
                  <ApplicationState source={source} />
                  <p className="evia-field-hint">{applicationHelp}</p>
                </dd>
              </div>
              <div>
                <dt>修订版本</dt>
                <dd>{source.revision}</dd>
              </div>
              <div>
                <dt>允许的 Origin</dt>
                <dd>
                  {source.kind === 'server'
                    ? '服务端来源不使用浏览器 Origin'
                    : source.allowed_origins.map((origin) => (
                        <div key={origin} style={{ overflowWrap: 'anywhere' }}>
                          <code>{origin}</code> <CopyButton value={origin} />
                        </div>
                      ))}
                </dd>
              </div>
            </dl>
            {source.kind === 'browser' ? (
              <p className="evia-field-hint">
                浏览器写入键用于标识来源，不是服务端密钥。接收请求必须携带允许列表中的精确 Origin。
              </p>
            ) : (
              <p className="evia-field-hint">
                已有服务端密钥不会再次显示。遗失时请轮换凭据，写入键和密钥需一起更新。
              </p>
            )}
            {editBase ? (
              <>
                {editError && <Failure error={editError} />}
                {conflict && (
                  <Alert tone="warning" title="修订版本冲突">
                    来源已被其他操作更新，当前输入已保留。请刷新核对最新配置，再采用新修订版本提交整个
                    Origin 列表。
                    <div>
                      <Button onClick={() => void detail.refetch()} loading={detail.isFetching}>
                        加载最新配置
                      </Button>
                      <Button
                        disabled={
                          detail.isFetching || !source || source.revision === editBase.revision
                        }
                        onClick={() => {
                          setEditBase(source);
                          setEditError(undefined);
                        }}
                      >
                        采用最新版本，保留输入
                      </Button>
                    </div>
                  </Alert>
                )}
                <form
                  id="source-edit"
                  className="console-form"
                  onSubmit={(event) => {
                    event.preventDefault();
                    if (busyRef.current || !editBase || conflict) return;
                    const parsed = parseSourceOrigins(editOrigins);
                    setOriginError(parsed.error);
                    if (parsed.error) {
                      editRef.current?.focus();
                      return;
                    }
                    setEditError(undefined);
                    void perform(
                      'edit',
                      async (isCurrent) => {
                        const result = await api.updateSource(
                          project.id,
                          editBase.id,
                          editBase.revision,
                          { allowed_origins: parsed.origins },
                          session.csrf_token,
                        );
                        if (!isCurrent()) return;
                        saved(result);
                        setEditBase(undefined);
                        setEditOrigins('');
                        toast.success('Origin 配置已保存，正在等待缓存窗口');
                      },
                      setEditError,
                    );
                  }}
                >
                  <Field
                    label="编辑允许的 Origin"
                    htmlFor="source-edit-origins"
                    required
                    error={originError}
                    hint={`基于修订版本 ${editBase.revision}，每行一个精确 Origin。`}
                  >
                    <Textarea
                      id="source-edit-origins"
                      ref={editRef}
                      rows={5}
                      required
                      value={editOrigins}
                      disabled={!!busy}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => {
                        setEditOrigins(event.target.value);
                        setOriginError(undefined);
                      }}
                      onInvalid={() => setOriginError('至少填写一个 Origin')}
                    />
                  </Field>
                  <div className="inline-actions">
                    <Button
                      onClick={() =>
                        guard.requestLeave(() => {
                          setEditBase(undefined);
                          setEditOrigins('');
                          setEditError(undefined);
                          setOriginError(undefined);
                        })
                      }
                      disabled={!!busy}
                    >
                      取消编辑
                    </Button>
                    <Button
                      variant="primary"
                      type="submit"
                      loading={busy === 'edit'}
                      disabled={!canWrite || !editDirty || conflict}
                    >
                      保存 Origin
                    </Button>
                  </div>
                </form>
              </>
            ) : (
              canWrite && (
                <div className="inline-actions">
                  {source.kind === 'browser' && (
                    <Button
                      disabled={!!busy || detail.isFetching}
                      onClick={() => {
                        setEditBase(source);
                        setEditOrigins(source.allowed_origins.join('\n'));
                        setEditError(undefined);
                        setOriginError(undefined);
                      }}
                    >
                      编辑 Origin
                    </Button>
                  )}
                  <Button
                    disabled={!!busy || detail.isFetching}
                    variant={source.status === 'active' ? 'danger' : 'secondary'}
                    onClick={() => {
                      setStatusTarget(source);
                      setStatusError(undefined);
                    }}
                  >
                    {source.status === 'active' ? '停用来源' : '启用来源'}
                  </Button>
                  <Button
                    disabled={!!busy || detail.isFetching}
                    onClick={() => {
                      setRotationTarget(source);
                      setOverlap('3600');
                      setRotationAttempt(undefined);
                      setRotationError(undefined);
                      setOverlapError(undefined);
                    }}
                  >
                    轮换凭据
                  </Button>
                </div>
              )
            )}
          </>
        ) : null}
      </Modal>

      <Modal
        title={statusTarget?.status === 'active' ? '停用来源？' : '启用来源？'}
        open={!!statusTarget}
        onRequestClose={() => {
          if (!busyRef.current) setStatusTarget(undefined);
        }}
        busy={busy === 'status'}
        initialFocusRef={statusCancelRef}
        footer={
          <>
            <Button
              ref={statusCancelRef}
              onClick={() => setStatusTarget(undefined)}
              disabled={!!busy}
            >
              取消
            </Button>
            <Button
              variant={statusTarget?.status === 'active' ? 'danger' : 'primary'}
              loading={busy === 'status'}
              disabled={!canWrite || !!statusError}
              onClick={() => {
                if (!statusTarget) return;
                const target = statusTarget;
                const body: SourceUpdate = {
                  status: target.status === 'active' ? 'disabled' : 'active',
                };
                setStatusError(undefined);
                void perform(
                  'status',
                  async (isCurrent) => {
                    const result = await api.updateSource(
                      project.id,
                      target.id,
                      target.revision,
                      body,
                      session.csrf_token,
                    );
                    if (!isCurrent()) return;
                    saved(result);
                    setStatusTarget(undefined);
                    toast.success(
                      `来源已${result.status === 'active' ? '启用' : '停用'}，正在等待缓存窗口`,
                    );
                  },
                  setStatusError,
                );
              }}
            >
              {statusTarget?.status === 'active' ? '确认停用' : '确认启用'}
            </Button>
          </>
        }
      >
        {statusError && (
          <>
            <Failure error={statusError} />
            <p>请取消后刷新详情核对状态，再决定是否继续。</p>
          </>
        )}
        <p>
          {statusTarget?.status === 'active'
            ? `停用「${statusTarget.name}」后，新事件会在配置缓存失效后被拒绝，已接收的数据会保留。`
            : `启用「${statusTarget?.name ?? ''}」后，该来源可在缓存窗口结束且发布配置具备接收资格时接收事件。`}
        </p>
        <p className="evia-field-hint">{applicationHelp}</p>
      </Modal>

      <Modal
        title="轮换凭据"
        open={!!rotationTarget}
        onRequestClose={closeRotation}
        busy={busy === 'rotation'}
        initialFocusRef={rotationCancelRef}
        footer={
          <>
            <Button ref={rotationCancelRef} onClick={closeRotation} disabled={!!busy}>
              取消
            </Button>
            <Button
              variant="primary"
              type="submit"
              form="source-rotation"
              loading={busy === 'rotation'}
              disabled={!canWrite}
            >
              {rotationAttempt ? '重试本次轮换' : '确认轮换'}
            </Button>
          </>
        }
      >
        {rotationError && <Failure error={rotationError} />}
        <p>
          轮换「{rotationTarget?.name}」会生成新的写入键
          {rotationTarget?.kind === 'server' ? '和服务端密钥' : ''}
          。请在重叠期内更新接入配置，过期后旧凭据将失效。重叠期为 0
          时，旧凭据将在缓存失效后被拒绝。
        </p>
        {rotationAttempt && (
          <Alert tone="warning" title="保留本次幂等重试">
            本次请求已发送，结果不确定时请使用“重试本次轮换”，它会复用相同幂等键和重叠期。关闭后重新轮换会创建另一组凭据。
          </Alert>
        )}
        <form
          id="source-rotation"
          className="console-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!rotationTarget || busyRef.current) return;
            const seconds = Number(overlap);
            if (
              !/^\d+$/.test(overlap) ||
              !Number.isInteger(seconds) ||
              seconds < 0 ||
              seconds > 86400
            ) {
              setOverlapError('请输入 0 到 86400 之间的整数秒');
              overlapRef.current?.focus();
              return;
            }
            const attempt = rotationAttempt ?? { key: crypto.randomUUID(), seconds };
            setRotationAttempt(attempt);
            setRotationError(undefined);
            setOverlapError(undefined);
            const id = rotationTarget.id;
            void perform(
              'rotation',
              async (isCurrent) => {
                const result = await api.rotateCredential(
                  project.id,
                  id,
                  { overlap_seconds: attempt.seconds },
                  attempt.key,
                  session.csrf_token,
                );
                if (!isCurrent()) return;
                receiveCredentials(result);
                setRotationTarget(undefined);
                setRotationAttempt(undefined);
                setOverlap('3600');
                toast.success('凭据已轮换，请更新接入配置');
              },
              setRotationError,
            );
          }}
        >
          <Field
            label="旧凭据重叠期（秒）"
            htmlFor="source-overlap"
            required
            error={overlapError}
            hint="范围 0–86400 秒，默认 3600 秒。"
          >
            <Input
              ref={overlapRef}
              id="source-overlap"
              type="number"
              min={0}
              max={86400}
              step={1}
              required
              value={overlap}
              disabled={!!busy || !!rotationAttempt}
              onChange={(event) => {
                setOverlap(event.target.value);
                setOverlapError(undefined);
              }}
              onInvalid={() => setOverlapError('请输入 0 到 86400 之间的整数秒')}
            />
          </Field>
        </form>
      </Modal>

      <Modal
        title="保存服务端密钥"
        open={!!issued}
        onRequestClose={closeIssued}
        footer={
          <Button variant="primary" onClick={closeIssued}>
            完成
          </Button>
        }
      >
        {issued && (
          <>
            <Alert tone="warning" title="请立即安全保存密钥">
              此密钥仅在本次凭据签发时提供。关闭后无法从详情重新查看，请安全保存；遗失后需要轮换。
            </Alert>
            <p>
              来源：{issued.source.name}，环境：{environments[issued.source.environment]}
            </p>
            <div className="console-form">
              <Field label="写入键" htmlFor="issued-write-key">
                <Input id="issued-write-key" value={issued.source.write_key} readOnly />
                <CopyButton value={issued.source.write_key} label="复制写入键" />
              </Field>
              <Field
                label="服务端 Bearer 密钥"
                htmlFor="issued-secret"
                hint="只用于服务端请求，避免放入网页、URL 或日志。"
              >
                <Input
                  id="issued-secret"
                  type={secretVisible ? 'text' : 'password'}
                  autoComplete="off"
                  value={issued.secret}
                  readOnly
                  spellCheck={false}
                />
                <div className="inline-actions">
                  <Button onClick={() => setSecretVisible((current) => !current)}>
                    {secretVisible ? '隐藏密钥' : '显示密钥'}
                  </Button>
                  <CopyButton value={issued.secret} label="复制服务端密钥" />
                </div>
              </Field>
              <Checkbox
                checked={secretSaved}
                onChange={(event) => setSecretSaved(event.target.checked)}
              >
                我已安全保存写入键和服务端密钥
              </Checkbox>
            </div>
          </>
        )}
      </Modal>
      {guard.confirmation}
    </>
  );
}
