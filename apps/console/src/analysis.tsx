import {
  cloneElement,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from 'react';
import { useBlocker } from 'react-router-dom';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type QueryJob,
  type QueryRequest,
  type QueryResult,
  type Session,
} from '@evia/api-client';
import {
  Badge,
  Button,
  CodeBlock,
  CopyButton,
  DataSurface,
  DataTable,
  EmptyPanel,
  ErrorPanel,
  Field as SharedField,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  Panel,
  Select,
  Tabs,
  Textarea,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import { useDraftNavigation } from './draft-navigation';
import {
  buildRequest,
  fieldOperators,
  formatTime,
  initialForm,
  metricLabel,
  modes,
  operators,
  publishedFields,
  systemFields,
  type AnalysisForm,
  type AnalysisMode,
} from './analysis-model';
import { ResultChart } from './analysis-chart';

function Field({ label, children }: { label: ReactNode; children: ReactElement<{ id?: string }> }) {
  const id = useId();
  return (
    <SharedField label={label} htmlFor={id}>
      {cloneElement(children, { id })}
    </SharedField>
  );
}

type Snapshot = {
  scope: string;
  request: QueryRequest;
  name: string;
  job: QueryJob;
  result?: QueryResult;
  started: number;
};
const jobLabels: Record<QueryJob['state'], string> = {
  queued: '排队中',
  running: '执行中',
  succeeded: '已完成',
  failed: '执行失败',
  canceled: '已取消',
  expired: '已过期',
};
const activeJob = (job?: QueryJob) => job?.state === 'queued' || job?.state === 'running';
const zones = [...new Set(['UTC', 'Asia/Shanghai', ...Intl.supportedValuesOf('timeZone')])];
function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : '请求失败，请重试。';
}
function cell(value: unknown, type: string, timezone: string) {
  if (value === null) return <span aria-label="空值">null</span>;
  if (value === undefined) return <span aria-label="字段缺失">缺失</span>;
  if (type === 'datetime')
    return (
      <time dateTime={String(value)} title={String(value)}>
        {formatTime(value, timezone)}
      </time>
    );
  if (typeof value === 'object')
    return (
      <details>
        <summary>查看内容</summary>
        <CodeBlock label="事件内容">{JSON.stringify(value, null, 2)}</CodeBlock>
      </details>
    );
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'number') return value.toLocaleString('zh-CN', { maximumFractionDigits: 6 });
  return String(value);
}
export function AnalysisPage({
  project,
  session,
  environment,
}: {
  project: Project;
  session: Session;
  environment: Environment;
}) {
  const [form, setForm] = useState<AnalysisForm>(() => initialForm(project.timezone));
  const [baseline, setBaseline] = useState(() => JSON.stringify(initialForm(project.timezone)));
  const [storedSnapshot, setSnapshot] = useState<Snapshot>();
  const permissionKey = [...project.permissions].sort().join(',');
  const scope = `${project.id}:${environment}:${session.user_id}:${permissionKey}`;
  const snapshot = storedSnapshot?.scope === scope ? storedSnapshot : undefined;
  const cache = useQueryClient();
  const [submitting, setSubmitting] = useState(false);
  const [canceling, setCanceling] = useState(false);
  const [observing, setObserving] = useState(false);
  const [error, setError] = useState<unknown>();
  const [notice, setNotice] = useState('');
  const [discardOpen, setDiscardOpen] = useState(false);
  const [filterID, setFilterID] = useState(1);
  const [propertyEvent, setPropertyEvent] = useState('');
  const [propertyVersion, setPropertyVersion] = useState('');
  const [scopeEpoch, setScopeEpoch] = useState(0);
  const requestAbort = useRef<AbortController | null>(null);
  const pollAbort = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const pendingNavigation = useRef<(() => void) | null>(null);
  const afterCloseNavigation = useRef<(() => void) | null>(null);
  const navigationApproved = useRef(false);
  const keepEditing = useRef<HTMLButtonElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const { register } = useDraftNavigation();
  const dirty = JSON.stringify(form) !== baseline;
  const busy = submitting || canceling || activeJob(snapshot?.job);
  const canRead = project.permissions.includes('project.read');
  const canDetail = project.permissions.includes('project.write');
  const blockedDetails = (form.mode === 'events' || form.mode === 'journey') && !canDetail;
  const blocker = useBlocker(() => !navigationApproved.current && (dirty || busy));
  const catalog = useInfiniteQuery({
    queryKey: ['analysis-events', project.id, environment],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      api.listEventDefinitions(project.id, { environment, cursor: pageParam, limit: 100 }, signal),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    enabled: canRead,
  });
  const events = useMemo(
    () =>
      catalog.data?.pages
        .flatMap((page) => page.items)
        .filter((item) => item.published_versions.length > 0) ?? [],
    [catalog.data],
  );
  const version = useQuery({
    queryKey: ['analysis-version', project.id, environment, propertyEvent, propertyVersion],
    queryFn: ({ signal }) =>
      api.getEventVersion(project.id, propertyEvent, Number(propertyVersion), signal),
    enabled: canRead && !!propertyEvent && !!propertyVersion,
  });
  const propertyFields = useMemo(
    () =>
      version.data
        ? publishedFields(version.data.definition.schema, propertyEvent, Number(propertyVersion))
        : [],
    [version.data, propertyEvent, propertyVersion],
  );
  const filterFields = [
    ...propertyFields,
    ...form.filters.flatMap((filter) =>
      filter.property && !propertyFields.some((field) => field.id === filter.property!.id)
        ? [filter.property]
        : [],
    ),
  ].filter((field, index, all) => all.findIndex((item) => item.id === field.id) === index);
  const patch = <K extends keyof AnalysisForm>(key: K, value: AnalysisForm[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  function reportFailure(failure: unknown) {
    if (failure instanceof ApiError && failure.status === 401) {
      generation.current += 1;
      requestAbort.current?.abort();
      pollAbort.current?.abort();
      setSnapshot(undefined);
      setObserving(false);
      cache.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
      cache.setQueryData(['session'], null);
      return;
    }
    if (failure instanceof ApiError && failure.status === 403) {
      setSnapshot(undefined);
      setObserving(false);
    }
    if (failure instanceof ApiError && failure.code === 'QUERY_EXPIRED') {
      setSnapshot((previous) =>
        previous
          ? { ...previous, job: { ...previous.job, state: 'expired' }, result: undefined }
          : previous,
      );
      setObserving(false);
      return;
    }
    setError(failure);
  }

  useEffect(() => {
    generation.current += 1;
    requestAbort.current?.abort();
    pollAbort.current?.abort();
    const next = initialForm(project.timezone);
    setForm(next);
    setBaseline(JSON.stringify(next));
    setSnapshot(undefined);
    setObserving(false);
    setPropertyEvent('');
    setPropertyVersion('');
    setSubmitting(false);
    setCanceling(false);
    setError(undefined);
    setNotice('');
    setDiscardOpen(false);
    pendingNavigation.current = null;
    afterCloseNavigation.current = null;
    setScopeEpoch((epoch) => epoch + 1);
    return () => {
      generation.current += 1;
      requestAbort.current?.abort();
      pollAbort.current?.abort();
      pendingNavigation.current = null;
      afterCloseNavigation.current = null;
    };
  }, [project.id, environment, session.user_id, permissionKey]);

  useEffect(
    () =>
      register((proceed) => {
        if (!navigationApproved.current && (dirty || busy)) {
          if (afterCloseNavigation.current) return;
          pendingNavigation.current = proceed;
          setDiscardOpen(true);
        } else proceed();
      }),
    [register, dirty, busy],
  );
  useEffect(() => {
    if (blocker.state === 'blocked') setDiscardOpen(true);
  }, [blocker.state]);
  useEffect(() => {
    if (!dirty && !busy) return;
    const prevent = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', prevent);
    return () => window.removeEventListener('beforeunload', prevent);
  }, [dirty, busy]);

  useEffect(() => {
    if (!observing || !snapshot) return;
    const controller = new AbortController();
    pollAbort.current = controller;
    const id = snapshot.job.query_id,
      epoch = generation.current;
    const current = () => !controller.signal.aborted && epoch === generation.current;
    const wait = (milliseconds: number) =>
      new Promise<void>((resolve, reject) => {
        const abort = () => {
          clearTimeout(timer);
          reject(new DOMException('Aborted', 'AbortError'));
        };
        const timer = window.setTimeout(() => {
          controller.signal.removeEventListener('abort', abort);
          resolve();
        }, milliseconds);
        controller.signal.addEventListener('abort', abort, { once: true });
      });
    void (async () => {
      let job = snapshot.job;
      const deadline = Date.now() + 12 * 60 * 1000;
      try {
        while (current()) {
          if (job.state === 'succeeded') {
            const result = await api.getQueryResult(project.id, id, controller.signal);
            if (current()) {
              setSnapshot((previous) =>
                previous?.job.query_id === id ? { ...previous, job, result } : previous,
              );
              setObserving(false);
            }
            return;
          }
          if (!activeJob(job)) {
            if (current()) setObserving(false);
            return;
          }
          if (Date.now() >= deadline) {
            setNotice('已暂停自动检查，服务端查询可能仍在执行。可继续检查或取消查询。');
            setObserving(false);
            return;
          }
          await wait(Math.max(250, job.poll_after_ms ?? 1000));
          if (!current()) return;
          job = await api.getQuery(project.id, id, controller.signal);
          if (current())
            setSnapshot((previous) =>
              previous?.job.query_id === id ? { ...previous, job } : previous,
            );
        }
      } catch (failure) {
        if (current()) {
          reportFailure(failure);
          setObserving(false);
        }
      }
    })();
    return () => controller.abort();
  }, [observing, snapshot?.job.query_id, project.id, scopeEpoch]);

  async function submit() {
    if (submitting || activeJob(snapshot?.job) || blockedDetails || !canRead) return;
    let request: QueryRequest;
    try {
      request = buildRequest(form, environment);
    } catch (failure) {
      reportFailure(failure);
      formRef.current?.querySelector<HTMLElement>('input:invalid,select:invalid')?.focus();
      return;
    }
    const controller = new AbortController();
    requestAbort.current?.abort();
    requestAbort.current = controller;
    const epoch = generation.current;
    setSubmitting(true);
    setError(undefined);
    setNotice('');
    setObserving(false);
    try {
      const job = await api.createQuery(project.id, request, session.csrf_token, controller.signal);
      if (controller.signal.aborted || generation.current !== epoch) return;
      setSnapshot({
        scope,
        request,
        name: modes.find((mode) => mode.id === form.mode)!.label,
        job,
        started: Date.now(),
      });
      setBaseline(JSON.stringify(form));
      setObserving(true);
    } catch (failure) {
      if (!controller.signal.aborted && generation.current === epoch) reportFailure(failure);
    } finally {
      if (!controller.signal.aborted && generation.current === epoch) setSubmitting(false);
    }
  }
  async function cancel() {
    if (!snapshot || canceling || !activeJob(snapshot.job)) return;
    pollAbort.current?.abort();
    setObserving(false);
    setCanceling(true);
    setError(undefined);
    const controller = new AbortController();
    requestAbort.current = controller;
    const epoch = generation.current;
    try {
      const job = await api.cancelQuery(
        project.id,
        snapshot.job.query_id,
        session.csrf_token,
        controller.signal,
      );
      if (!controller.signal.aborted && epoch === generation.current) {
        setSnapshot((previous) => (previous ? { ...previous, job } : previous));
        setNotice(
          job.state === 'canceled' ? '查询已取消。' : '查询已完成，取消请求未改变已完成的结果。',
        );
        if (job.state === 'succeeded' || activeJob(job)) setObserving(true);
      }
    } catch (failure) {
      if (!controller.signal.aborted && epoch === generation.current) reportFailure(failure);
    } finally {
      if (!controller.signal.aborted && epoch === generation.current) setCanceling(false);
    }
  }
  function continueEditing() {
    setDiscardOpen(false);
    pendingNavigation.current = null;
    afterCloseNavigation.current = null;
    if (blocker.state === 'blocked') blocker.reset();
  }
  function leave() {
    afterCloseNavigation.current =
      blocker.state === 'blocked' ? blocker.proceed : pendingNavigation.current;
    pendingNavigation.current = null;
    setDiscardOpen(false);
  }
  function finishLeaving() {
    const proceed = afterCloseNavigation.current;
    afterCloseNavigation.current = null;
    if (!proceed) return;
    // Router navigation consumes this approval while replaying the confirmed intent.
    navigationApproved.current = true;
    try {
      proceed();
    } finally {
      navigationApproved.current = false;
    }
  }
  const eventOptions = [
    { value: '', label: '全部事件' },
    ...events.map((event) => ({
      value: event.event_key,
      label: `${event.display_name} (${event.event_key})`,
    })),
  ];
  const result = snapshot?.result;
  const rows: Record<string, unknown>[] = result?.rows ?? [];
  const columns: DataTableColumn<Record<string, unknown>>[] =
    result?.columns.map((column) => ({
      key: column.key,
      header:
        column.key === 'metric_0'
          ? metricLabel(snapshot!.request)
          : ({
              bucket: '时间',
              event_key: '事件',
              event_time: '事件时间',
              uid: '原始 UID',
              anonymous_id: '匿名标识',
              context: '上下文',
              properties: '属性',
              step_index: '步骤',
              subjects: '到达主体数',
              conversion_rate: '相对首步转化率',
            }[column.key] ??
            column.label ??
            column.key),
      render: (row) =>
        column.key === 'conversion_rate' && typeof row[column.key] === 'number'
          ? `${((row[column.key] as number) * 100).toFixed(2)}%`
          : column.key === 'step_index' && typeof row[column.key] === 'number'
            ? String((row[column.key] as number) + 1)
            : cell(row[column.key], column.type, snapshot!.request.timezone),
    })) ?? [];
  return (
    <>
      <PageHeading title="分析查询" />
      {!canRead ? (
        <EmptyPanel description="暂无查询权限，需要项目读取权限。" />
      ) : (
        <div className="section-stack">
          <Panel label="查询条件">
            <Tabs
              label="分析类型"
              value={form.mode}
              onChange={(value) => patch('mode', value as AnalysisMode)}
              items={modes.map((mode) => ({ id: mode.id, label: mode.label, content: null }))}
            />
            <form
              ref={formRef}
              onSubmit={(event) => {
                event.preventDefault();
                void submit();
              }}
              className="section-stack"
            >
              <div className="field-grid">
                <Field label="开始时间（UTC）">
                  <Input
                    type="datetime-local"
                    required
                    min="1970-01-01T00:00"
                    max="2100-12-31T23:59"
                    value={form.from}
                    onChange={(event) => patch('from', event.target.value)}
                  />
                </Field>
                <Field label="结束时间（UTC，不含）">
                  <Input
                    type="datetime-local"
                    required
                    min="1970-01-01T00:00"
                    max="2100-12-31T23:59"
                    value={form.to}
                    onChange={(event) => patch('to', event.target.value)}
                  />
                </Field>
                <Field label="统计和显示时区">
                  <Select
                    value={form.timezone}
                    onChange={(event) => patch('timezone', event.target.value)}
                    options={zones.map((zone) => ({ value: zone, label: zone }))}
                  />
                </Field>
                <Field label="最多结果行">
                  <Input
                    type="number"
                    min="1"
                    max="500"
                    required
                    value={form.limit}
                    onChange={(event) => patch('limit', event.target.value)}
                  />
                </Field>
              </div>
              <p className="field-help">
                时间范围为左闭右开，
                {form.mode === 'metrics' || form.mode === 'trend' ? '最多 31 天' : '最多 7 天'}
                。输入采用 UTC，分桶与结果采用所选时区。
              </p>
              {catalog.isPending ? (
                <LoadingPanel label="正在加载已发布事件" />
              ) : catalog.isError ? (
                <ErrorPanel message={catalog.error.message} retry={() => void catalog.refetch()} />
              ) : (
                <>
                  {form.mode !== 'funnel' && (
                    <div className="field-grid">
                      <Field label="事件（跨已发布版本）">
                        <Select
                          value={
                            form.metric === 'pv' &&
                            (form.mode === 'metrics' || form.mode === 'trend')
                              ? 'sys.page_view'
                              : form.event
                          }
                          disabled={
                            (form.mode === 'metrics' || form.mode === 'trend') &&
                            form.metric === 'pv'
                          }
                          onChange={(event) => patch('event', event.target.value)}
                          options={
                            eventOptions.some((item) => item.value === 'sys.page_view')
                              ? eventOptions
                              : [
                                  ...eventOptions,
                                  { value: 'sys.page_view', label: '页面访问 (sys.page_view)' },
                                ]
                          }
                        />
                      </Field>
                      {(form.mode === 'metrics' || form.mode === 'trend') && (
                        <Field label="指标">
                          <Select
                            value={form.metric}
                            onChange={(event) =>
                              patch('metric', event.target.value as AnalysisForm['metric'])
                            }
                            options={[
                              { value: 'count', label: '事件数' },
                              { value: 'pv', label: 'PV（sys.page_view 事件数）' },
                              { value: 'uv', label: 'UV（去重主体数）' },
                            ]}
                          />
                        </Field>
                      )}
                      {form.mode === 'trend' && (
                        <Field label="时间粒度">
                          <Select
                            value={form.interval}
                            onChange={(event) =>
                              patch('interval', event.target.value as AnalysisForm['interval'])
                            }
                            options={[
                              { value: 'minute', label: '分钟' },
                              { value: 'hour', label: '小时' },
                              { value: 'day', label: '天' },
                            ]}
                          />
                        </Field>
                      )}
                    </div>
                  )}
                  {(form.mode === 'journey' ||
                    form.mode === 'funnel' ||
                    ((form.mode === 'metrics' || form.mode === 'trend') &&
                      form.metric === 'uv')) && (
                    <>
                      <div className="field-grid">
                        <Field label="统计主体">
                          <Select
                            value={form.subject}
                            onChange={(event) =>
                              patch('subject', event.target.value as AnalysisForm['subject'])
                            }
                            options={[
                              { value: 'uid', label: '原始 UID' },
                              { value: 'anonymous', label: '原始匿名标识' },
                            ]}
                          />
                        </Field>
                        {form.mode === 'journey' && (
                          <Field label={form.subject === 'uid' ? '原始 UID' : '匿名标识（UUID）'}>
                            <Input
                              required
                              maxLength={form.subject === 'uid' ? 128 : 36}
                              value={form.identity}
                              onChange={(event) => patch('identity', event.target.value)}
                              autoComplete="off"
                            />
                          </Field>
                        )}
                        {(form.mode === 'metrics' || form.mode === 'trend') && (
                          <Field label="UV 精度">
                            <Select
                              value={form.accuracy}
                              onChange={(event) =>
                                patch('accuracy', event.target.value as AnalysisForm['accuracy'])
                              }
                              options={[
                                { value: 'exact', label: '精确' },
                                { value: 'approximate', label: '近似' },
                              ]}
                            />
                          </Field>
                        )}
                      </div>
                      <p className="field-help">
                        {form.subject === 'uid'
                          ? '仅使用事实原始 UID，排除未设置或空 UID，不关联登录前的匿名事件。'
                          : '使用事实原始匿名标识，可能包含登录后保留该标识的事件，同一设备不代表同一人。'}
                        {form.mode === 'trend' && ' 各时间桶 UV 不能相加作为整段 UV。'}
                      </p>
                    </>
                  )}
                  {form.mode === 'funnel' && (
                    <>
                      <div className="editor-list" aria-label="有序漏斗步骤">
                        {form.steps.map((step, index) => (
                          <div className="editor-row" key={index}>
                            <span>步骤 {index + 1}</span>
                            <Field label={`步骤 ${index + 1} 事件`}>
                              <Select
                                required
                                value={step.event}
                                onChange={(event) => {
                                  const eventKey = event.target.value;
                                  patch(
                                    'steps',
                                    form.steps.map((item, position) =>
                                      position === index
                                        ? {
                                            event: eventKey,
                                            version: String(
                                              events
                                                .find(
                                                  (definition) => definition.event_key === eventKey,
                                                )
                                                ?.published_versions.at(-1) ?? '',
                                            ),
                                          }
                                        : item,
                                    ),
                                  );
                                }}
                                options={[
                                  { value: '', label: '选择已发布事件' },
                                  ...eventOptions.slice(1),
                                ]}
                              />
                            </Field>
                            <Field label={`步骤 ${index + 1} 版本`}>
                              <Select
                                required
                                value={step.version}
                                onChange={(event) =>
                                  patch(
                                    'steps',
                                    form.steps.map((item, position) =>
                                      position === index
                                        ? { ...item, version: event.target.value }
                                        : item,
                                    ),
                                  )
                                }
                                options={[
                                  { value: '', label: '选择版本' },
                                  ...(
                                    events.find((event) => event.event_key === step.event)
                                      ?.published_versions ?? []
                                  ).map((version) => ({
                                    value: String(version),
                                    label: `v${version}`,
                                  })),
                                ]}
                              />
                            </Field>
                            <div className="editor-row-actions">
                              <Button
                                size="small"
                                aria-label={`上移步骤 ${index + 1}`}
                                disabled={index === 0}
                                onClick={() => {
                                  const next = [...form.steps];
                                  [next[index - 1], next[index]] = [next[index]!, next[index - 1]!];
                                  patch('steps', next);
                                }}
                              >
                                上移
                              </Button>
                              <Button
                                size="small"
                                aria-label={`下移步骤 ${index + 1}`}
                                disabled={index === form.steps.length - 1}
                                onClick={() => {
                                  const next = [...form.steps];
                                  [next[index + 1], next[index]] = [next[index]!, next[index + 1]!];
                                  patch('steps', next);
                                }}
                              >
                                下移
                              </Button>
                              <Button
                                size="small"
                                aria-label={`删除步骤 ${index + 1}`}
                                disabled={form.steps.length <= 2}
                                onClick={() =>
                                  patch(
                                    'steps',
                                    form.steps.filter((_, position) => position !== index),
                                  )
                                }
                              >
                                删除
                              </Button>
                            </div>
                          </div>
                        ))}
                      </div>
                      <Toolbar
                        actions={
                          <Button
                            disabled={form.steps.length >= 8}
                            onClick={() =>
                              patch('steps', [...form.steps, { event: '', version: '' }])
                            }
                          >
                            添加步骤
                          </Button>
                        }
                      >
                        <Field label="转化窗口（秒）">
                          <Input
                            type="number"
                            required
                            min="1"
                            max="604800"
                            value={form.window}
                            onChange={(event) => patch('window', event.target.value)}
                          />
                        </Field>
                      </Toolbar>
                      <p className="field-help">
                        按顺序到达步骤，允许穿插其他事件；每个主体每步最多计一次，窗口从候选首步开始。同一条事件不能占据两个步骤。
                      </p>
                    </>
                  )}
                  {catalog.hasNextPage && (
                    <Button
                      loading={catalog.isFetchingNextPage}
                      onClick={() => void catalog.fetchNextPage()}
                    >
                      加载更多已发布事件
                    </Button>
                  )}
                </>
              )}
              <Panel
                title="过滤条件"
                action={
                  <Button
                    disabled={form.filters.length >= 20}
                    onClick={() => {
                      patch('filters', [
                        ...form.filters,
                        { id: filterID, field: 'event_key', op: 'eq', value: '' },
                      ]);
                      setFilterID((id) => id + 1);
                    }}
                  >
                    添加条件
                  </Button>
                }
              >
                <div className="field-grid">
                  <Field label="添加属性字段的事件">
                    <Select
                      value={propertyEvent}
                      onChange={(event) => {
                        const key = event.target.value;
                        setPropertyEvent(key);
                        setPropertyVersion(
                          String(
                            events
                              .find((item) => item.event_key === key)
                              ?.published_versions.at(-1) ?? '',
                          ),
                        );
                      }}
                      options={[{ value: '', label: '仅系统字段' }, ...eventOptions.slice(1)]}
                    />
                  </Field>
                  {propertyEvent && (
                    <Field label="属性所属已发布版本">
                      <Select
                        value={propertyVersion}
                        onChange={(event) => setPropertyVersion(event.target.value)}
                        options={[
                          { value: '', label: '选择版本' },
                          ...(
                            events.find((item) => item.event_key === propertyEvent)
                              ?.published_versions ?? []
                          ).map((value) => ({ value: String(value), label: `v${value}` })),
                        ]}
                      />
                    </Field>
                  )}
                </div>
                {propertyEvent && version.isFetching && (
                  <LoadingPanel label="正在读取已发布属性类型" />
                )}
                {propertyEvent && version.isError && (
                  <ErrorPanel
                    message={version.error.message}
                    retry={() => void version.refetch()}
                  />
                )}
                {propertyEvent && version.data && !propertyFields.length && (
                  <p className="field-help">此版本没有可查询的属性字段。</p>
                )}
                <p className="field-help">
                  属性类型来自所选不可变版本，字段只匹配该事件及版本。存在包含显式 null，缺失不包含
                  null。
                </p>
                {form.filters.length > 1 && (
                  <Field label="条件关系">
                    <Select
                      value={form.filterJoin}
                      onChange={(event) => patch('filterJoin', event.target.value as 'and' | 'or')}
                      options={[
                        { value: 'and', label: '全部满足' },
                        { value: 'or', label: '任一满足' },
                      ]}
                    />
                  </Field>
                )}
                {!form.filters.length && <p className="field-help">未添加过滤条件。</p>}
                <div className="editor-list">
                  {form.filters.map((filter, index) => (
                    <div className="editor-row" key={filter.id}>
                      <Field label={`条件 ${index + 1} 字段`}>
                        <Select
                          value={filter.field}
                          onChange={(event) =>
                            patch(
                              'filters',
                              form.filters.map((item) =>
                                item.id === filter.id
                                  ? {
                                      ...item,
                                      field: event.target.value,
                                      property: filterFields.find(
                                        (field) => field.id === event.target.value,
                                      ),
                                      op: 'eq',
                                      value: '',
                                    }
                                  : item,
                              ),
                            )
                          }
                          options={[
                            ...systemFields.map((field) => ({
                              value: field.key,
                              label: `${field.label} (${field.type})`,
                            })),
                            ...filterFields.map((field) => ({
                              value: field.id,
                              label: field.label,
                            })),
                          ]}
                        />
                      </Field>
                      <Field label={`条件 ${index + 1} 运算`}>
                        <Select
                          value={filter.op}
                          onChange={(event) =>
                            patch(
                              'filters',
                              form.filters.map((item) =>
                                item.id === filter.id ? { ...item, op: event.target.value } : item,
                              ),
                            )
                          }
                          options={fieldOperators(filter.field, filter.property?.type).map(
                            (op) => ({ value: op, label: operators[op] ?? op }),
                          )}
                        />
                      </Field>
                      {filter.op !== 'is_missing' && filter.op !== 'is_present' && (
                        <Field
                          label={`条件 ${index + 1} 值${filter.op === 'in' || (filter.property?.type.startsWith('array:') && filter.op !== 'contains') ? '（JSON 数组，最多 20 项）' : filter.field === 'event_time' ? '（RFC3339，含时区）' : ''}`}
                        >
                          {filter.op === 'in' ||
                          (filter.property?.type.startsWith('array:') &&
                            filter.op !== 'contains') ? (
                            <Textarea
                              placeholder='例如 []、["value"]、[1, 2]'
                              value={filter.value}
                              onChange={(event) =>
                                patch(
                                  'filters',
                                  form.filters.map((item) =>
                                    item.id === filter.id
                                      ? { ...item, value: event.target.value }
                                      : item,
                                  ),
                                )
                              }
                            />
                          ) : filter.property?.type === 'boolean' ||
                            filter.property?.type === 'array:boolean' ? (
                            <Select
                              required
                              value={filter.value}
                              onChange={(event) =>
                                patch(
                                  'filters',
                                  form.filters.map((item) =>
                                    item.id === filter.id
                                      ? { ...item, value: event.target.value }
                                      : item,
                                  ),
                                )
                              }
                              options={[
                                { value: '', label: '选择布尔值' },
                                { value: 'true', label: 'true' },
                                { value: 'false', label: 'false' },
                              ]}
                            />
                          ) : (
                            <Input
                              required={
                                filter.field === 'event_time' ||
                                (!!filter.property && !filter.property.type.endsWith('string'))
                              }
                              type={
                                filter.property?.type === 'number' ||
                                filter.property?.type === 'integer' ||
                                filter.property?.type === 'array:number' ||
                                filter.property?.type === 'array:integer'
                                  ? 'number'
                                  : 'text'
                              }
                              step={filter.property?.type.includes('integer') ? '1' : 'any'}
                              maxLength={1024}
                              value={filter.value}
                              onChange={(event) =>
                                patch(
                                  'filters',
                                  form.filters.map((item) =>
                                    item.id === filter.id
                                      ? { ...item, value: event.target.value }
                                      : item,
                                  ),
                                )
                              }
                            />
                          )}
                        </Field>
                      )}
                      <Button
                        size="small"
                        aria-label={`删除条件 ${index + 1}`}
                        onClick={() =>
                          patch(
                            'filters',
                            form.filters.filter((item) => item.id !== filter.id),
                          )
                        }
                      >
                        删除
                      </Button>
                    </div>
                  ))}
                </div>
              </Panel>
              {blockedDetails && (
                <p role="status">
                  事件明细和用户旅程需要项目编辑权限，当前可使用指标、趋势和漏斗。
                </p>
              )}
              <Toolbar
                actions={
                  <>
                    <Button
                      type="submit"
                      variant="primary"
                      loading={submitting}
                      disabled={busy || blockedDetails || catalog.isPending || catalog.isError}
                    >
                      运行查询
                    </Button>
                    {activeJob(snapshot?.job) && (
                      <Button loading={canceling} onClick={() => void cancel()}>
                        取消查询
                      </Button>
                    )}
                  </>
                }
              >
                <span className="field-help">
                  {dirty ? '条件已修改，运行后生成新快照。' : '查询结果为有界快照。'}
                </span>
              </Toolbar>
            </form>
          </Panel>
          {error !== undefined && (
            <ErrorPanel
              message={errorMessage(error)}
              requestID={error instanceof ApiError ? error.requestID : undefined}
              retry={
                snapshot &&
                !observing &&
                !submitting &&
                (activeJob(snapshot.job) ||
                  (snapshot.job.state === 'succeeded' && !snapshot.result))
                  ? () => {
                      setError(undefined);
                      setNotice('');
                      setObserving(true);
                    }
                  : undefined
              }
            />
          )}
          {snapshot ? (
            <Panel
              title="查询结果"
              action={
                <Badge
                  tone={
                    snapshot.job.state === 'failed' || snapshot.job.state === 'expired'
                      ? 'error'
                      : snapshot.job.state === 'succeeded'
                        ? 'success'
                        : 'neutral'
                  }
                >
                  {jobLabels[snapshot.job.state]}
                </Badge>
              }
            >
              <div className="section-stack">
                <div className="query-summary">
                  <strong>
                    {snapshot.name}
                    {snapshot.request.kind === 'trend' ? `，${metricLabel(snapshot.request)}` : ''}
                  </strong>
                  <span>
                    {formatTime(snapshot.request.time_range.from, snapshot.request.timezone)} 至{' '}
                    {formatTime(snapshot.request.time_range.to, snapshot.request.timezone)}
                    （不含），{snapshot.request.timezone}
                  </span>
                  <span>
                    环境：{snapshot.request.environment}，
                    {snapshot.request.event_keys?.join('、') ||
                      (snapshot.request.kind === 'funnel' ? '按步骤事件及版本' : '全部事件')}
                    {snapshot.request.subject
                      ? `，${snapshot.request.subject === 'uid' ? '原始 UID' : '原始匿名标识'}`
                      : ''}
                  </span>
                  {dirty && <span>上方条件已修改，下方仍对应这次提交的查询。</span>}
                </div>
                <details>
                  <summary>查询标识与原始条件</summary>
                  <CopyButton value={snapshot.job.query_id} label="复制查询标识" />
                  <CodeBlock label="本次查询参数">
                    {JSON.stringify(snapshot.request, null, 2)}
                  </CodeBlock>
                </details>
                <div role="status" aria-live="polite">
                  {notice ||
                    (observing
                      ? snapshot.job.state === 'succeeded'
                        ? '正在获取结果。'
                        : '按服务端间隔检查查询状态。'
                      : '')}
                </div>
                {activeJob(snapshot.job) && !observing && (
                  <Button
                    onClick={() => {
                      setError(undefined);
                      setNotice('');
                      setObserving(true);
                    }}
                  >
                    继续检查状态
                  </Button>
                )}
                {snapshot.job.state === 'failed' && (
                  <p role="alert">
                    查询执行失败：{snapshot.job.error?.code ?? 'UNKNOWN_ERROR'}。
                    {snapshot.job.error?.retryable
                      ? '可保留条件重新运行。'
                      : '请调整条件后重新运行。'}
                    {snapshot.job.error?.field_paths?.join('、')}
                  </p>
                )}
                {snapshot.job.state === 'canceled' && (
                  <EmptyPanel description="查询已取消，可调整条件后重新运行。" />
                )}
                {snapshot.job.state === 'expired' && (
                  <EmptyPanel description="查询已过期，结果不会恢复，请重新运行查询。" />
                )}
                {result && (
                  <>
                    <dl className="detail-list">
                      <dt>精度</dt>
                      <dd>
                        {result.metadata.accuracy === 'exact'
                          ? '精确'
                          : result.metadata.accuracy === 'approximate'
                            ? '近似'
                            : '不适用'}
                        ，采样率 {(result.metadata.sample_rate * 100).toLocaleString('zh-CN')}%
                      </dd>
                      <dt>覆盖状态</dt>
                      <dd>
                        {
                          { provisional: '暂定', settled: '已稳定', partial: '部分覆盖' }[
                            result.metadata.coverage.state
                          ]
                        }
                        ，
                        {formatTime(
                          result.metadata.coverage.time_range.from,
                          snapshot.request.timezone,
                        )}{' '}
                        至{' '}
                        {formatTime(
                          result.metadata.coverage.time_range.to,
                          snapshot.request.timezone,
                        )}
                        （不含）
                      </dd>
                      <dt>已知缺口</dt>
                      <dd>
                        {result.metadata.coverage.known_gaps.length
                          ? result.metadata.coverage.known_gaps.join('、')
                          : '未报告'}
                      </dd>
                      <dt>实际观察到的最晚接收时间</dt>
                      <dd>
                        {result.metadata.data_watermark
                          ? formatTime(result.metadata.data_watermark, snapshot.request.timezone)
                          : '无观测数据'}
                        ，不代表数据已完整到达
                      </dd>
                      <dt>生成时间</dt>
                      <dd>
                        {formatTime(result.metadata.generated_at, snapshot.request.timezone)}，
                        {result.metadata.cache_hit ? '命中缓存' : '未命中缓存'}
                      </dd>
                    </dl>
                    {snapshot.request.kind === 'trend' &&
                      !snapshot.request.interval &&
                      rows.length > 0 && (
                        <div className="metric-grid">
                          <div>
                            <span>{metricLabel(snapshot.request)}</span>
                            <div className="metric-value">
                              {cell(rows[0]!.metric_0, 'number', snapshot.request.timezone)}
                            </div>
                          </div>
                        </div>
                      )}
                    {rows.length > 0 &&
                      ((snapshot.request.kind === 'trend' && snapshot.request.interval) ||
                        snapshot.request.kind === 'funnel') && (
                        <ResultChart
                          kind={snapshot.request.kind === 'funnel' ? 'funnel' : 'trend'}
                          rows={rows}
                          label={
                            snapshot.request.kind === 'funnel'
                              ? '到达主体数'
                              : metricLabel(snapshot.request)
                          }
                          timezone={snapshot.request.timezone}
                          steps={snapshot.request.funnel?.steps.map(
                            (step, index) =>
                              `${index + 1}. ${step.event_key} v${step.schema_version}`,
                          )}
                        />
                      )}
                    {snapshot.request.kind === 'trend' && snapshot.request.interval && (
                      <p className="field-help">
                        仅展示实际返回的时间桶，缺失时间桶未补零。各桶 UV 不能相加作为整段 UV。
                      </p>
                    )}
                    <DataSurface label="查询结果表">
                      <DataTable
                        caption="查询结果，数值与图表对应"
                        rows={rows}
                        columns={columns}
                        rowKey={(row) => String(rows.indexOf(row))}
                        empty="该范围内无匹配结果，不等同于数值零。"
                      />
                    </DataSurface>
                    <p className="field-help">
                      返回 {rows.length} 行，上限 {snapshot.request.limit}{' '}
                      行。超出预算会明确失败，此版本不支持结果翻页。
                    </p>
                  </>
                )}
              </div>
            </Panel>
          ) : (
            <EmptyPanel description="尚未运行查询，选择条件并运行，结果会显示本次查询的时间、环境与精度。" />
          )}
        </div>
      )}
      <Modal
        open={discardOpen}
        title="离开分析查询？"
        onRequestClose={continueEditing}
        afterClose={finishLeaving}
        initialFocusRef={keepEditing}
        footer={
          <>
            <Button ref={keepEditing} onClick={continueEditing}>
              继续编辑
            </Button>
            <Button variant="danger" onClick={leave}>
              离开页面
            </Button>
          </>
        }
      >
        <p>
          {busy
            ? '离开会停止本页的状态检查，已提交的服务端查询仍可能继续执行。需要停止执行时，请先取消查询。'
            : '尚未运行的新条件会丢失。'}
        </p>
      </Modal>
    </>
  );
}
