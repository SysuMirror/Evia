import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type SavedAnalysis,
  type SavedAnalysisCreate,
  type SavedAnalysisDefinition,
  type SavedAnalysisDraft,
  type SavedAnalysisPublish,
  type SavedAnalysisValidation,
  type SavedAnalysisVersion,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  CodeBlock,
  CopyButton,
  DataSurface,
  DataTable,
  EmptyPanel,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Panel,
  Select,
  Tabs,
  Toolbar,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import type { CatalogPageProps } from './catalog';
import { CatalogError, environmentLabel } from './catalog-helpers';
import { useCatalogScope } from './catalog-scope';
import { useUnsavedWork } from './use-unsaved-work';
import { formatTime } from './analysis-model';
import {
  initialSavedAnalysisDefinition,
  savedDefinitionSummary,
  savedAnalysisIssues,
} from './saved-analysis-model';
import { SavedAnalysisEditor } from './saved-analysis-editor';
import { SavedAnalysisRun, type SavedRunWork } from './saved-analysis-run';

type Workspace = { scope: string; draft?: SavedAnalysisDraft; definition: SavedAnalysisDefinition };
type CreateAttempt = { key: string; body: SavedAnalysisCreate };
type PublishAttempt = { key: string; analysisID: string; body: SavedAnalysisPublish };
const uncertain = (error: unknown) =>
  error instanceof ApiError && (error.status === 0 || error.status >= 500 || error.status === 408);
const conflict = (error: unknown) =>
  error instanceof ApiError &&
  [
    'SAVED_ANALYSIS_REVISION_CONFLICT',
    'PUBLISHED_VERSION_CONFLICT',
    'VALIDATION_HASH_CONFLICT',
  ].includes(error.code);
const categories: Record<string, string> = {
  definition: '初始定义',
  metadata: '标题与描述',
  kind: '分析类型',
  event_versions: '固定事件版本',
  filter: '筛选条件',
  subject: '统计主体',
  metrics: '指标',
  group_by: '分组',
  interval: '时间粒度',
  funnel: '漏斗',
  limit: '结果上限',
  visualization: '展示方式',
};
export function SavedAnalysesPage({ project, environment, session }: CatalogPageProps) {
  const cache = useQueryClient(),
    toast = useToast(),
    id = useId();
  const canRead = project.permissions.includes('project.read'),
    canWrite = project.permissions.includes('project.write');
  const identity = JSON.stringify([
    project.id,
    environment,
    session.user_id,
    session.csrf_token,
    [...project.permissions].sort(),
  ]);
  const prefix = ['saved-analyses', project.id, environment, session.user_id] as const;
  const [cursors, setCursors] = useState<string[]>([]),
    [versionCursors, setVersionCursors] = useState<string[]>([]);
  const [storedWorkspace, setWorkspace] = useState<Workspace>();
  const workspace = storedWorkspace?.scope === identity ? storedWorkspace : undefined;
  const [tab, setTab] = useState('draft');
  const [busy, setBusy] = useState('');
  const [denied, setDenied] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [latest, setLatest] = useState<SavedAnalysisDraft>();
  const [validation, setValidation] = useState<SavedAnalysisValidation>();
  const [validationOpen, setValidationOpen] = useState(false),
    [acknowledged, setAcknowledged] = useState(false);
  const [createAttempt, setCreateAttempt] = useState<CreateAttempt>();
  const [publishAttempt, setPublishAttempt] = useState<PublishAttempt>();
  const [versionNumber, setVersionNumber] = useState<number>();
  const [displayVersion, setDisplayVersion] = useState<SavedAnalysisVersion>();
  const [editorValid, setEditorValid] = useState(true),
    [editorPending, setEditorPending] = useState(false);
  const [runWork, setRunWork] = useState<SavedRunWork>({ dirty: false, busy: false });
  const request = useRef<AbortController | null>(null),
    operation = useRef(false);
  const scope = useCatalogScope(identity, canRead && !denied, { project, session });
  const dirty =
    !!workspace &&
    (!workspace.draft ||
      JSON.stringify(workspace.definition) !== JSON.stringify(workspace.draft.definition) ||
      editorPending);
  const guard = useUnsavedWork({
    dirty: dirty || !!createAttempt || !!publishAttempt || runWork.dirty,
    busy: !!busy || runWork.busy,
  });
  const list = useQuery({
    queryKey: [...prefix, 'list', cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listSavedAnalyses(project.id, { environment, cursor: cursors.at(-1), limit: 20 }, signal),
    enabled: canRead && !denied,
  });
  const versions = useQuery({
    queryKey: [...prefix, 'versions', workspace?.draft?.analysis_id, versionCursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listSavedAnalysisVersions(
        project.id,
        workspace!.draft!.analysis_id,
        { cursor: versionCursors.at(-1), limit: 20 },
        signal,
      ),
    enabled: !!workspace?.draft && tab === 'versions' && !denied,
  });
  const selectedVersion = useQuery({
    queryKey: [...prefix, 'version', workspace?.draft?.analysis_id, versionNumber],
    queryFn: ({ signal }) =>
      api.getSavedAnalysisVersion(
        project.id,
        workspace!.draft!.analysis_id,
        versionNumber!,
        signal,
      ),
    enabled: !!workspace?.draft && !!versionNumber && tab === 'versions' && !denied,
  });
  useEffect(() => {
    setWorkspace(undefined);
    setCursors([]);
    setVersionCursors([]);
    setVersionNumber(undefined);
    setDisplayVersion(undefined);
    setValidation(undefined);
    setValidationOpen(false);
    setAcknowledged(false);
    setCreateAttempt(undefined);
    setPublishAttempt(undefined);
    setLatest(undefined);
    setError(null);
    setBusy('');
    setDenied(false);
    setEditorPending(false);
    setEditorValid(true);
    setRunWork({ dirty: false, busy: false });
    operation.current = false;
    return () => request.current?.abort();
  }, [identity]);
  const deny = useCallback(
    (failure: Error) => {
      scope.invalidate();
      request.current?.abort();
      setDenied(true);
      setWorkspace(undefined);
      setValidation(undefined);
      setValidationOpen(false);
      setCreateAttempt(undefined);
      setPublishAttempt(undefined);
      setLatest(undefined);
      setError(failure);
      setBusy('');
      operation.current = false;
      void cache.cancelQueries({
        queryKey: ['saved-analyses', project.id, environment, session.user_id],
      });
      cache.removeQueries({
        queryKey: ['saved-analyses', project.id, environment, session.user_id],
      });
    },
    [scope.invalidate, cache, project.id, environment, session.user_id],
  );
  useEffect(() => {
    for (const failure of [list.error, versions.error, selectedVersion.error])
      if (failure instanceof ApiError && [401, 403].includes(failure.status)) {
        deny(failure);
        break;
      }
  }, [list.error, versions.error, selectedVersion.error, deny]);
  useEffect(() => {
    if (
      selectedVersion.data?.analysis_id === workspace?.draft?.analysis_id &&
      selectedVersion.data?.environment === environment
    )
      setDisplayVersion(selectedVersion.data);
  }, [selectedVersion.data, workspace?.draft?.analysis_id, environment]);
  const onRunWork = useCallback((work: SavedRunWork) => setRunWork(work), []);
  async function perform<T>(
    label: string,
    action: (signal: AbortSignal) => Promise<T>,
    success: (value: T) => void,
    failure?: (error: Error) => void,
  ) {
    if (operation.current || denied) return;
    operation.current = true;
    const token = scope.capture(),
      controller = new AbortController();
    request.current = controller;
    setBusy(label);
    setError(null);
    try {
      const value = await action(controller.signal);
      if (!controller.signal.aborted && scope.isCurrent(token)) success(value);
    } catch (caught) {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        const next = caught instanceof Error ? caught : new Error('请求失败，请重试。');
        if (next instanceof ApiError && [401, 403].includes(next.status)) deny(next);
        else {
          setError(next);
          failure?.(next);
        }
      }
    } finally {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        setBusy('');
        operation.current = false;
      }
    }
  }
  function resetReview() {
    setValidation(undefined);
    setAcknowledged(false);
    setPublishAttempt(undefined);
  }
  function openDraft(analysis: SavedAnalysis) {
    if (operation.current) return;
    guard.requestLeave(() => {
      void perform(
        '读取草稿',
        (signal) => api.getSavedAnalysisDraft(project.id, analysis.analysis_id, signal),
        (draft) => {
          setWorkspace({ scope: identity, draft, definition: structuredClone(draft.definition) });
          setLatest(undefined);
          resetReview();
          setCreateAttempt(undefined);
          setVersionNumber(draft.published_version || undefined);
          setVersionCursors([]);
          setTab('draft');
          setDisplayVersion(undefined);
          setEditorPending(false);
          setEditorValid(true);
        },
      );
    });
  }
  function create() {
    if (operation.current) return;
    guard.requestLeave(() => {
      setWorkspace({ scope: identity, definition: initialSavedAnalysisDefinition() });
      setTab('draft');
      setDisplayVersion(undefined);
      setLatest(undefined);
      resetReview();
      setCreateAttempt(undefined);
      setVersionNumber(undefined);
      setDisplayVersion(undefined);
      setEditorValid(true);
      setEditorPending(false);
      setError(null);
    });
  }
  function save() {
    if (operation.current || busy) return;
    if (
      !workspace ||
      !canWrite ||
      !editorValid ||
      editorPending ||
      publishAttempt ||
      savedAnalysisIssues(workspace.definition).length
    )
      return;
    if (!workspace.draft) {
      const next = createAttempt ?? {
        key: crypto.randomUUID(),
        body: { environment, definition: structuredClone(workspace.definition) },
      };
      setCreateAttempt(next);
      void perform(
        '创建分析',
        (signal) =>
          api.createSavedAnalysis(project.id, next.body, next.key, session.csrf_token, signal),
        (draft) => {
          setWorkspace({ scope: identity, draft, definition: structuredClone(draft.definition) });
          setCreateAttempt(undefined);
          resetReview();
          setCursors([]);
          void cache.invalidateQueries({ queryKey: prefix });
          toast.success('分析草稿已创建');
        },
        (failure) => {
          if (!uncertain(failure)) setCreateAttempt(undefined);
        },
      );
    } else {
      const draft = workspace.draft,
        definition = structuredClone(workspace.definition);
      void perform(
        '保存草稿',
        (signal) =>
          api.updateSavedAnalysisDraft(
            project.id,
            draft.analysis_id,
            draft.draft_revision,
            { definition },
            session.csrf_token,
            signal,
          ),
        (saved) => {
          setWorkspace({
            scope: identity,
            draft: saved,
            definition: structuredClone(saved.definition),
          });
          setLatest(undefined);
          resetReview();
          void cache.invalidateQueries({ queryKey: prefix });
          toast.success('草稿已保存');
        },
        (failure) => {
          if (conflict(failure)) resetReview();
        },
      );
    }
  }
  function readLatest() {
    if (!workspace?.draft) return;
    void perform(
      '读取最新草稿',
      (signal) => api.getSavedAnalysisDraft(project.id, workspace.draft!.analysis_id, signal),
      (draft) => {
        setLatest(draft);
        resetReview();
      },
    );
  }
  function validate() {
    if (!workspace?.draft || dirty || editorPending || !editorValid || publishAttempt) return;
    const draft = workspace.draft;
    void perform(
      '校验已保存草稿',
      (signal) =>
        api.validateSavedAnalysis(
          project.id,
          draft.analysis_id,
          {
            draft_revision: draft.draft_revision,
            expected_published_version: draft.published_version,
          },
          session.csrf_token,
          signal,
        ),
      (checked) => {
        setValidation(checked);
        setValidationOpen(true);
        setAcknowledged(false);
      },
    );
  }
  function publish() {
    if (operation.current || busy) return;
    if (!workspace?.draft || !validation || !canWrite || !acknowledged || dirty) return;
    const next = publishAttempt ?? {
      key: crypto.randomUUID(),
      analysisID: workspace.draft.analysis_id,
      body: {
        draft_revision: validation.draft_revision,
        expected_published_version: validation.published_version,
        validation_hash: validation.validation_hash,
      },
    };
    setPublishAttempt(next);
    void perform(
      '发布版本',
      (signal) =>
        api.publishSavedAnalysis(
          project.id,
          next.analysisID,
          next.body,
          next.key,
          session.csrf_token,
          signal,
        ),
      (published) => {
        setWorkspace((previous) =>
          previous?.draft
            ? { ...previous, draft: { ...previous.draft, published_version: published.version } }
            : previous,
        );
        setVersionNumber(published.version);
        setVersionCursors([]);
        setValidationOpen(false);
        resetReview();
        setTab('versions');
        cache.setQueryData(
          [...prefix, 'version', published.analysis_id, published.version],
          published,
        );
        void cache.invalidateQueries({ queryKey: prefix });
        toast.success(`已发布不可变版本 v${published.version}`);
      },
      (failure) => {
        if (!uncertain(failure)) resetReview();
      },
    );
  }
  const columns: DataTableColumn<SavedAnalysis>[] = [
    {
      key: 'title',
      header: '分析',
      render: (analysis) => (
        <Button variant="link" onClick={() => openDraft(analysis)} disabled={!!busy}>
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
      render: (analysis) => formatTime(analysis.updated_at, project.timezone),
    },
    {
      key: 'action',
      header: '操作',
      render: (analysis) => (
        <Button size="small" disabled={!!busy} onClick={() => openDraft(analysis)}>
          打开分析
        </Button>
      ),
    },
  ];
  const versionItems = versions.data?.items ?? [];
  const options = [...versionItems];
  if (
    selectedVersion.data &&
    !options.some((item) => item.version === selectedVersion.data.version)
  )
    options.push(selectedVersion.data);
  options.sort((left, right) => left.version - right.version);
  const frozen = !!busy || !!createAttempt || !!publishAttempt || runWork.busy;
  const draftContent = workspace && (
    <div className="section-stack saved-analysis-detail">
      {workspace.draft && (
        <Toolbar
          actions={
            <Button onClick={readLatest} disabled={!!busy || !!publishAttempt}>
              读取最新草稿
            </Button>
          }
        >
          <div className="inline-actions">
            <Badge tone="neutral">草稿 r{workspace.draft.draft_revision}</Badge>
            <Badge tone={workspace.draft.published_version ? 'success' : 'neutral'}>
              {workspace.draft.published_version
                ? `最新发布 v${workspace.draft.published_version}`
                : '尚未发布'}
            </Badge>
            {dirty && <Badge tone="warning">未保存</Badge>}
          </div>
        </Toolbar>
      )}
      <SavedAnalysisEditor
        key={`${workspace.draft?.analysis_id ?? 'new'}:${latest && !dirty ? latest.draft_revision : ''}`}
        value={workspace.definition}
        onChange={(definition) => {
          setWorkspace((previous) => (previous ? { ...previous, definition } : previous));
          resetReview();
        }}
        projectID={project.id}
        actorID={session.user_id}
        environment={environment}
        readOnly={!canWrite}
        disabled={frozen}
        onValidityChange={setEditorValid}
        onPendingChange={setEditorPending}
      />
      <form
        id={`${id}-draft`}
        className="section-stack"
        onSubmit={(event) => {
          event.preventDefault();
          save();
        }}
      >
        <Toolbar
          actions={
            <>
              {canWrite && (
                <Button
                  type="submit"
                  variant="primary"
                  loading={busy === '创建分析' || busy === '保存草稿'}
                  disabled={
                    frozen || !editorValid || editorPending || (!!workspace.draft && !dirty)
                  }
                >
                  {workspace.draft ? '保存草稿' : '创建分析'}
                </Button>
              )}
              {workspace.draft && (
                <Button
                  onClick={validate}
                  loading={busy === '校验已保存草稿'}
                  disabled={frozen || dirty || !editorValid || editorPending}
                >
                  校验已保存草稿
                </Button>
              )}
            </>
          }
        >
          <span className="field-help">
            {dirty
              ? '先保存修改，再校验与发布。'
              : '校验仅检查已保存的配置与固定依赖，不执行查询。'}
          </span>
        </Toolbar>
      </form>
      {publishAttempt && !validationOpen && (
        <Alert
          tone="warning"
          title="发布响应尚未确认"
          action={<Button onClick={() => setValidationOpen(true)}>继续确认发布</Button>}
        >
          已保留相同的校验计数、摘要与请求标识。
        </Alert>
      )}
      {createAttempt && !busy && (
        <Alert
          tone="warning"
          title="创建响应尚未确认"
          action={<Button onClick={save}>重试创建分析</Button>}
        >
          已保留原始定义与请求标识，重试不会重复创建。
        </Alert>
      )}
      {error && conflict(error) && !latest && (
        <Alert
          tone="warning"
          title="远端修订已变化"
          action={
            <Button onClick={readLatest} disabled={!!busy}>
              读取最新草稿
            </Button>
          }
        >
          本地修改已保留。读取最新草稿后，选择继续保存本地内容或采用远端内容。
        </Alert>
      )}
      {latest && (
        <Panel title={`最新草稿 r${latest.draft_revision}`}>
          <div className="section-stack">
            <p>
              本地内容已保留，远端最新发布为{' '}
              {latest.published_version ? `v${latest.published_version}` : '尚未发布'}。
            </p>
            <details>
              <summary>查看最新草稿内容</summary>
              <CodeBlock>{JSON.stringify(latest.definition, null, 2)}</CodeBlock>
            </details>
            <div className="inline-actions">
              <Button
                disabled={frozen}
                onClick={() => {
                  setWorkspace((previous) =>
                    previous ? { ...previous, draft: latest } : previous,
                  );
                  setLatest(undefined);
                  resetReview();
                  setError(null);
                }}
              >
                以最新修订继续编辑本地内容
              </Button>
              <Button
                disabled={frozen}
                onClick={() =>
                  guard.requestLeave(() => {
                    setWorkspace({
                      scope: identity,
                      draft: latest,
                      definition: structuredClone(latest.definition),
                    });
                    setLatest(undefined);
                    resetReview();
                    setError(null);
                    setEditorPending(false);
                    setEditorValid(true);
                  })
                }
              >
                采用最新草稿
              </Button>
            </div>
          </div>
        </Panel>
      )}
      {workspace.draft && (
        <details>
          <summary>资源标识与定义</summary>
          <CopyButton value={workspace.draft.analysis_id} label="复制分析标识" />
          <CodeBlock>{JSON.stringify(workspace.draft.definition, null, 2)}</CodeBlock>
        </details>
      )}
    </div>
  );
  const publishedContent = workspace?.draft && (
    <div className="section-stack saved-analysis-published">
      {workspace.draft.published_version === 0 ? (
        <EmptyPanel description="尚无发布版本。保存草稿并校验后，可发布第一个不可变版本。" />
      ) : (
        <>
          <Panel label="选择已发布版本" className="saved-analysis-version-picker">
            <Toolbar>
              <label htmlFor={`${id}-version`}>已发布版本</label>
              <Select
                id={`${id}-version`}
                value={versionNumber ? String(versionNumber) : ''}
                disabled={!!busy || runWork.busy || !!runWork.retryPending || versions.isFetching}
                onChange={(event) => setVersionNumber(Number(event.target.value))}
                options={[
                  { value: '', label: '选择版本' },
                  ...options.map((item) => ({
                    value: String(item.version),
                    label: `v${item.version}，${item.definition.title}${item.version === workspace.draft!.published_version ? '（最新发布）' : ''}`,
                  })),
                ]}
              />
              {displayVersion?.analysis_id === workspace.draft.analysis_id && (
                <div className="saved-analysis-version-metadata">
                  <Badge tone="success">不可变版本 v{displayVersion.version}</Badge>
                  <span>{savedDefinitionSummary(displayVersion.definition)}</span>
                  <details>
                    <summary>版本来源</summary>
                    <dl className="detail-list">
                      <div>
                        <dt>草稿修订</dt>
                        <dd>r{displayVersion.draft_revision}</dd>
                      </div>
                      <div>
                        <dt>发布时间</dt>
                        <dd>
                          {formatTime(displayVersion.created_at, project.timezone)}，
                          {project.timezone}
                        </dd>
                      </div>
                    </dl>
                  </details>
                </div>
              )}
            </Toolbar>
            <CatalogError error={versions.error} />
            {(versionCursors.length > 0 || !!versions.data?.next_cursor) && (
              <PaginationBar
                page={versionCursors.length + 1}
                count={versionItems.length}
                hasNext={!!versions.data?.next_cursor}
                busy={versions.isFetching || !!busy || runWork.busy}
                onPrevious={() => setVersionCursors((previous) => previous.slice(0, -1))}
                onNext={() => {
                  if (versions.data?.next_cursor)
                    setVersionCursors((previous) => [...previous, versions.data!.next_cursor!]);
                }}
              />
            )}
          </Panel>
          {selectedVersion.isPending && <LoadingPanel label="正在读取不可变版本" />}
          {selectedVersion.isError && (
            <CatalogError
              error={selectedVersion.error}
              retry={() => void selectedVersion.refetch()}
            />
          )}
          {displayVersion?.analysis_id === workspace.draft.analysis_id && (
            <>
              <SavedAnalysisRun
                key={displayVersion.analysis_id}
                disabled={selectedVersion.isFetching || versionNumber !== displayVersion.version}
                version={displayVersion}
                project={project}
                environment={environment}
                session={session}
                onWorkChange={onRunWork}
                onAccessDenied={deny}
              />
              <details>
                <summary>查看该版本的固定配置与依赖</summary>
                <CodeBlock>
                  {JSON.stringify(
                    {
                      definition: displayVersion.definition,
                      dependencies: displayVersion.dependencies,
                    },
                    null,
                    2,
                  )}
                </CodeBlock>
              </details>
            </>
          )}
        </>
      )}
    </div>
  );
  if (!canRead)
    return (
      <>
        <PageHeading title="保存的分析" />
        <EmptyPanel description="暂无读取分析的权限。" />
      </>
    );
  if (denied)
    return (
      <>
        <PageHeading title="保存的分析" />
        <CatalogError error={error} />
      </>
    );
  return (
    <div className="saved-analysis-workspace section-stack">
      <PageHeading
        title={
          workspace
            ? workspace.draft
              ? workspace.definition.title || '分析草稿'
              : '新建分析'
            : '保存的分析'
        }
        action={
          workspace ? (
            <Button
              disabled={!!busy || runWork.busy}
              onClick={() =>
                guard.requestLeave(() => {
                  setWorkspace(undefined);
                  setRunWork({ dirty: false, busy: false });
                  setError(null);
                  setLatest(undefined);
                  resetReview();
                  setCreateAttempt(undefined);
                })
              }
            >
              返回列表
            </Button>
          ) : canWrite ? (
            <Button variant="primary" onClick={create} disabled={!!busy}>
              新建分析
            </Button>
          ) : undefined
        }
      />
      <CatalogError error={error} />
      {busy === '读取草稿' && <LoadingPanel label="正在读取草稿" />}
      {workspace ? (
        <Panel label="分析工作区" className="saved-analysis-tabs">
          <Tabs
            label="分析版本"
            value={tab}
            onChange={(next) => {
              if (!busy && !runWork.busy && !createAttempt && !publishAttempt && next !== tab) {
                if (editorPending || (tab === 'versions' && runWork.dirty))
                  guard.requestLeave(() => setTab(next));
                else setTab(next);
              }
            }}
            items={[
              { id: 'draft', label: '草稿', content: tab === 'draft' ? draftContent : null },
              {
                id: 'versions',
                label: '已发布版本',
                disabled: !workspace.draft,
                content: tab === 'versions' ? publishedContent : null,
              },
            ]}
          />
        </Panel>
      ) : (
        <DataSurface label="保存的分析列表">
          <CatalogError error={list.error} retry={() => void list.refetch()} />
          <DataTable
            caption="当前项目和环境的保存分析"
            columns={columns}
            rows={list.data?.items ?? []}
            rowKey={(row) => row.analysis_id}
            loading={list.isPending}
            empty={
              canWrite
                ? '暂无保存的分析，新建分析后可保存配置并发布版本。'
                : '当前环境暂无保存的分析。'
            }
          />
          <PaginationBar
            page={cursors.length + 1}
            count={list.data?.items.length ?? 0}
            hasNext={!!list.data?.next_cursor}
            busy={list.isFetching || !!busy}
            onPrevious={() => setCursors((previous) => previous.slice(0, -1))}
            onNext={() => {
              if (list.data?.next_cursor)
                setCursors((previous) => [...previous, list.data!.next_cursor!]);
            }}
          />
        </DataSurface>
      )}
      <Modal
        open={validationOpen}
        title="发布前校验"
        onRequestClose={() => {
          if (!busy) setValidationOpen(false);
        }}
        busy={!!busy}
        footer={
          <>
            <Button disabled={!!busy} onClick={() => setValidationOpen(false)}>
              返回草稿
            </Button>
            {canWrite && (
              <Button
                variant="primary"
                loading={busy === '发布版本'}
                disabled={!!busy || !acknowledged || !validation || dirty}
                onClick={publish}
              >
                {publishAttempt ? '重试发布版本' : '发布不可变版本'}
              </Button>
            )}
          </>
        }
      >
        <CatalogError error={error} />
        {validation && (
          <div className="section-stack saved-analysis-validation">
            <div className="inline-actions">
              <Badge tone="success">校验通过</Badge>
              <span>
                草稿 r{validation.draft_revision}，基于
                {validation.published_version
                  ? `已发布 v${validation.published_version}`
                  : '首次发布'}
              </span>
            </div>
            <p className="field-help">
              校验未执行查询。发布后配置与依赖不可修改，实际运行仍受权限与查询预算约束。
            </p>
            <h3>固定事件依赖</h3>
            <DataTable
              caption="本次发布的精确事件版本"
              rows={validation.dependencies.event_versions}
              columns={[
                { key: 'event', header: '事件', render: (dependency) => dependency.event_key },
                {
                  key: 'version',
                  header: '固定版本',
                  render: (dependency) => `v${dependency.schema_version}`,
                },
                {
                  key: 'hash',
                  header: '定义摘要',
                  render: (dependency) => (
                    <details>
                      <summary>查看摘要</summary>
                      <code>{dependency.definition_hash}</code>
                    </details>
                  ),
                },
              ]}
              rowKey={(dependency) => `${dependency.event_key}:${dependency.schema_version}`}
            />
            <h3>相对上次发布的语义变化</h3>
            {validation.diff.length ? (
              <DataTable
                caption="发布前语义差异"
                rows={validation.diff}
                columns={[
                  {
                    key: 'category',
                    header: '变更',
                    render: (change) => categories[change.category] ?? change.category,
                  },
                  { key: 'path', header: '位置', render: (change) => <code>{change.path}</code> },
                  {
                    key: 'values',
                    header: '内容',
                    render: (change) => (
                      <details>
                        <summary>查看前后内容</summary>
                        <CodeBlock>
                          {JSON.stringify({ before: change.before, after: change.after }, null, 2)}
                        </CodeBlock>
                      </details>
                    ),
                  },
                ]}
                rowKey={(change) => `${change.category}:${change.path}`}
              />
            ) : (
              <p>定义与上次发布一致。</p>
            )}
            <details>
              <summary>完整归一化定义与校验摘要</summary>
              <CodeBlock>{JSON.stringify(validation, null, 2)}</CodeBlock>
            </details>
            {canWrite && (
              <Checkbox
                checked={acknowledged}
                disabled={!!busy || !!publishAttempt}
                onChange={(event) => setAcknowledged(event.target.checked)}
              >
                已核对语义差异与固定事件版本
              </Checkbox>
            )}
            {publishAttempt && !busy && (
              <Alert tone="warning" title="发布响应尚未确认">
                重试使用相同的校验计数、摘要与请求标识。
              </Alert>
            )}
          </div>
        )}
      </Modal>
      {guard.confirmation}
    </div>
  );
}
