import {
  cloneElement,
  useEffect,
  useId,
  useLayoutEffect,
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
  ResultHeader,
  Select,
  Tabs,
  Textarea,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import { useDraftNavigation } from './draft-navigation';
import {
  AnalysisValidationError,
  buildRequest,
  fieldOperators,
  formatTime,
  initialForm,
  metricLabel,
  modes,
  operators,
  publishedFields,
  systemFields,
  validateAnalysisSection,
  type AnalysisEditorSection,
  type AnalysisForm,
  type AnalysisMode,
  type AnalysisValidationTarget,
} from './analysis-model';
import { ResultChart } from './analysis-chart';

function Field({
  label,
  children,
  error,
}: {
  label: ReactNode;
  children: ReactElement<{ id?: string }>;
  error?: string;
}) {
  const id = useId();
  return (
    <SharedField label={label} htmlFor={id} error={error}>
      {cloneElement(children, { id })}
    </SharedField>
  );
}

type Snapshot = {
  scope: string;
  request: QueryRequest;
  name: string;
  semanticMetric?: AnalysisForm['metric'];
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
  const [validationError, setValidationError] = useState('');
  const [validationTarget, setValidationTarget] = useState<AnalysisValidationTarget>();
  const [editor, setEditor] = useState<AnalysisEditorSection | null>(null);
  const operation = useRef(false);
  const rowFocus = useRef<{ source: HTMLElement; selector: string } | null>(null);
  const queryFormID = useId();
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
  const scalarSegmentFields = useMemo(
    () =>
      propertyFields.filter((field) =>
        ['string', 'boolean', 'number', 'integer'].includes(field.type),
      ),
    [propertyFields],
  );
  const filterFields = [
    ...propertyFields,
    ...form.filters.flatMap((filter) =>
      filter.property && !propertyFields.some((field) => field.id === filter.property!.id)
        ? [filter.property]
        : [],
    ),
  ].filter((field, index, all) => all.findIndex((item) => item.id === field.id) === index);
  const patch = <K extends keyof AnalysisForm>(key: K, value: AnalysisForm[K]) => {
    setValidationError('');
    setValidationTarget(undefined);
    setForm((current) => ({ ...current, [key]: value }));
  };

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
    setEditor(null);
    setValidationError('');
    operation.current = false;
    rowFocus.current = null;
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

  useLayoutEffect(() => {
    const pending = rowFocus.current;
    rowFocus.current = null;
    if (!pending) return;
    const owned =
      document.activeElement === pending.source ||
      (!pending.source.isConnected && document.activeElement === document.body);
    if (!owned) return;
    document.querySelector<HTMLElement>(`dialog[open] ${pending.selector}`)?.focus();
  }, [form.filters, form.steps]);

  function requestRowFocus(source: HTMLElement, selector: string) {
    if (document.activeElement === source) rowFocus.current = { source, selector };
  }

  function finishEditor(section: AnalysisEditorSection) {
    try {
      validateAnalysisSection(form, section);
      setValidationError('');
      setValidationTarget(undefined);
      setEditor(null);
    } catch (failure) {
      setValidationError(errorMessage(failure));
      setValidationTarget(section);
    }
  }

  async function submit() {
    if (operation.current || submitting || activeJob(snapshot?.job) || blockedDetails || !canRead)
      return;
    let request: QueryRequest;
    try {
      request = buildRequest(form, environment);
      if (
        (form.mode === 'metrics' || form.mode === 'trend') &&
        form.metric === 'pv' &&
        !events.some((event) => event.event_key === form.event)
      )
        throw new AnalysisValidationError('event', '请选择代表页面访问的已发布事件，再统计 PV。');
    } catch (failure) {
      if (failure instanceof AnalysisValidationError) {
        setError(undefined);
        setValidationError(failure.message);
        setValidationTarget(failure.target);
        if (
          failure.target === 'event' ||
          failure.target === 'identity' ||
          failure.target === 'interval'
        ) {
          formRef.current
            ?.querySelector<HTMLElement>(`[data-query-control="${failure.target}"]`)
            ?.focus();
        } else setEditor(failure.target);
      } else reportFailure(failure);
      return;
    }
    const controller = new AbortController();
    requestAbort.current?.abort();
    requestAbort.current = controller;
    const epoch = generation.current;
    operation.current = true;
    setSubmitting(true);
    setError(undefined);
    setNotice('');
    setObserving(false);
    try {
      const job = await api.createQuery(project.id, request, session.csrf_token, controller.signal);
      if (controller.signal.aborted || generation.current !== epoch) return;
      setSnapshot({
        scope,
        semanticMetric: form.mode === 'metrics' || form.mode === 'trend' ? form.metric : undefined,
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
      if (!controller.signal.aborted && generation.current === epoch) {
        operation.current = false;
        setSubmitting(false);
      }
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
  const isMetric = form.mode === 'metrics' || form.mode === 'trend';
  const usesSubject =
    form.mode === 'journey' ||
    form.mode === 'funnel' ||
    form.mode === 'retention' ||
    form.mode === 'segment' ||
    form.mode === 'path' ||
    form.mode === 'attribution' ||
    (isMetric && form.metric === 'uv');
  const filterSummary = `${form.filterJoin === 'and' ? '全部满足' : '任一满足'}：${form.filters
    .map((filter) => {
      const label =
        filter.property?.label ??
        systemFields.find((field) => field.key === filter.field)?.label ??
        filter.field;
      return `${label} ${operators[filter.op] ?? filter.op}${filter.op === 'is_missing' || filter.op === 'is_present' ? '' : ` ${filter.value === '' ? '""' : filter.value}`}`;
    })
    .join('，')}`;
  const result = snapshot?.result;
  const rows: Record<string, unknown>[] = result?.rows ?? [];
  const columns: DataTableColumn<Record<string, unknown>>[] =
    result?.columns.map((column) => ({
      key: column.key,
      header:
        column.key === 'metric_0'
          ? metricLabel(snapshot!.request, snapshot!.semanticMetric)
          : ({
              bucket: '时间',
              event_key: '事件',
              event_time: '事件时间',
              uid: '原始 UID',
              anonymous_id: '匿名标识',
              context: '上下文',
              properties: '属性',
              step_index: '步骤',
              subjects: snapshot!.request.kind === 'attribution' ? '转化主体数' : '到达主体数',
              conversion_rate: '相对首步转化率',
              cohort_index: '队列序号',
              cohort_start: '队列起始',
              cohort_size: '队列规模',
              retained: '各期留存主体数',
              retention_rate: '各期留存率',
              segment_value: '分群标签',
              share: snapshot!.request.kind === 'attribution' ? '归因占比' : '群体占比',
              path: '事件路径',
              touchpoint: '归因触点',
            }[column.key] ??
            column.label ??
            column.key),
      render: (row) =>
        column.key === 'conversion_rate' && typeof row[column.key] === 'number'
          ? `${((row[column.key] as number) * 100).toFixed(2)}%`
          : column.key === 'step_index' && typeof row[column.key] === 'number'
            ? String((row[column.key] as number) + 1)
            : column.key === 'share' && typeof row[column.key] === 'number'
              ? `${((row[column.key] as number) * 100).toFixed(2)}%`
              : column.key === 'retention_rate' && Array.isArray(row[column.key])
                ? (row[column.key] as unknown[])
                    .map((value) =>
                      typeof value === 'number' ? `${(value * 100).toFixed(2)}%` : String(value),
                    )
                    .join('，')
                : column.key === 'retained' && Array.isArray(row[column.key])
                  ? (row[column.key] as unknown[]).map((value) => String(value)).join('，')
                  : column.key === 'path' && Array.isArray(row[column.key])
                    ? (row[column.key] as unknown[]).map((value) => String(value)).join(' → ')
                    : cell(row[column.key], column.type, snapshot!.request.timezone),
    })) ?? [];
  const queryEditor = (
    <form
      id={queryFormID}
      ref={formRef}
      onSubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
      className="analysis-query-form"
    >
      {catalog.isPending ? (
        <LoadingPanel label="正在加载已发布事件" />
      ) : catalog.isError ? (
        <ErrorPanel message={catalog.error.message} retry={() => void catalog.refetch()} />
      ) : (
        <div className="analysis-query-bar">
          {form.mode !== 'funnel' &&
            form.mode !== 'retention' &&
            form.mode !== 'segment' &&
            form.mode !== 'path' &&
            form.mode !== 'attribution' && (
              <Field
                label="事件（跨已发布版本）"
                error={validationTarget === 'event' ? validationError : undefined}
              >
                <Select
                  data-query-control="event"
                  value={form.event}
                  required={
                    (form.mode === 'metrics' || form.mode === 'trend') && form.metric === 'pv'
                  }
                  onChange={(event) => patch('event', event.target.value)}
                  options={eventOptions}
                />
              </Field>
            )}
          {(form.mode === 'metrics' || form.mode === 'trend') && (
            <Field label="指标">
              <Select
                value={form.metric}
                onChange={(event) => patch('metric', event.target.value as AnalysisForm['metric'])}
                options={[
                  { value: 'count', label: '事件数' },
                  { value: 'pv', label: 'PV（所选页面访问事件数）' },
                  { value: 'uv', label: 'UV（去重主体数）' },
                ]}
              />
            </Field>
          )}
          {form.mode === 'trend' && (
            <Field
              label="时间粒度"
              error={validationTarget === 'interval' ? validationError : undefined}
            >
              <Select
                data-query-control="interval"
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
          {usesSubject && (
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
          )}
          {form.mode === 'journey' && (
            <Field
              label={form.subject === 'uid' ? '原始 UID' : '匿名标识（UUID）'}
              error={validationTarget === 'identity' ? validationError : undefined}
            >
              <Input
                required
                maxLength={form.subject === 'uid' ? 128 : 36}
                data-query-control="identity"
                value={form.identity}
                onChange={(event) => patch('identity', event.target.value)}
                autoComplete="off"
              />
            </Field>
          )}
          {form.mode === 'funnel' && (
            <div className="analysis-funnel-overview">
              <h3>有序漏斗</h3>
              <Button onClick={() => setEditor('funnel')} aria-haspopup="dialog">
                配置漏斗（{form.steps.length} 步）
              </Button>
              <span className="field-help">
                {form.steps
                  .map(
                    (step, index) =>
                      `${index + 1}. ${step.event ? `${step.event} v${step.version}` : '未选择事件'}`,
                  )
                  .join(' → ')}
              </span>
            </div>
          )}
          {form.mode === 'retention' && (
            <div className="analysis-funnel-overview">
              <h3>留存分析</h3>
              <Button onClick={() => setEditor('retention')} aria-haspopup="dialog">
                配置留存
              </Button>
              <span className="field-help">
                起始：
                {form.retentionStart.event
                  ? `${form.retentionStart.event} v${form.retentionStart.version}`
                  : '未选择事件'}
                ；回访：
                {form.retentionReturn.event
                  ? `${form.retentionReturn.event} v${form.retentionReturn.version}`
                  : '未选择事件'}
                ；{form.retentionGranularity === 'day' ? '按天' : '按周'} {form.retentionPeriods} 期
              </span>
            </div>
          )}
          {form.mode === 'segment' && (
            <div className="analysis-funnel-overview">
              <h3>主体分群</h3>
              <Button
                onClick={() => {
                  setPropertyEvent(form.segmentEvent.event);
                  setPropertyVersion(form.segmentEvent.version);
                  setEditor('segment');
                }}
                aria-haspopup="dialog"
              >
                配置分群
              </Button>
              <span className="field-help">
                {form.segmentEvent.event
                  ? `${form.segmentEvent.event} v${form.segmentEvent.version}`
                  : '未选择事件'}
                ；字段：{form.segmentProperty ? form.segmentProperty.label : '未选择字段'}
              </span>
            </div>
          )}
          {form.mode === 'path' && (
            <div className="analysis-funnel-overview">
              <h3>聚合路径</h3>
              <Button onClick={() => setEditor('path')} aria-haspopup="dialog">
                配置路径
              </Button>
              <span className="field-help">
                深度：{form.pathDepth} 个事件；起始：
                {form.pathStart.event
                  ? `${form.pathStart.event} v${form.pathStart.version}`
                  : '窗口内最早事件'}
              </span>
            </div>
          )}
          {form.mode === 'attribution' && (
            <div className="analysis-funnel-overview">
              <h3>归因分析</h3>
              <Button onClick={() => setEditor('attribution')} aria-haspopup="dialog">
                配置归因
              </Button>
              <span className="field-help">
                转化：
                {form.attributionConversion.event
                  ? `${form.attributionConversion.event} v${form.attributionConversion.version}`
                  : '未选择事件'}
                ；触点 {form.attributionTouchpoints.length} 个；窗口 {form.attributionWindow} 秒
              </span>
            </div>
          )}
        </div>
      )}
      {isMetric && form.metric === 'pv' && (
        <p className="field-help">选择代表页面访问的已发布事件，按该事件的发生次数统计 PV。</p>
      )}
      {usesSubject && (
        <p className="field-help analysis-subject-help">
          {form.subject === 'uid'
            ? '按事实原始 UID 统计，不关联登录前的匿名事件。'
            : '按事实原始匿名标识统计，同一设备不代表同一人。'}
        </p>
      )}
      {form.mode === 'retention' && (
        <p className="field-help">
          主体在最早起始事件所在日历周期入队，0 期留存恒为
          100%；周粒度以周一为起点，时间范围不能超过期数乘以粒度（最多 84 天）。
        </p>
      )}
      {form.mode === 'segment' && (
        <p className="field-help">
          每个主体按其最早一条分群事件的标量字段值归入唯一群体，后续事件不会使其换组；缺失或空值归入固定缺失组。字段限字符串、布尔或数值，时间范围最多
          7 天。
        </p>
      )}
      {form.mode === 'path' && (
        <p className="field-help">
          统计主体实际遍历的有序事件序列，每个主体最多贡献一条固定长度路径；事件数不足深度的主体被排除，结果按主体数降序排列，时间范围最多
          7 天。
        </p>
      )}
      {form.mode === 'attribution' && (
        <p className="field-help">
          末次触点模型：每个转化主体在回溯窗口内记到因果序最晚的触点，窗口内无触点的转化归入直接来源；每个主体最多归因一次，结果按转化主体数降序排列，时间范围最多
          7 天。
        </p>
      )}
      {catalog.hasNextPage && (
        <Button
          size="small"
          loading={catalog.isFetchingNextPage}
          onClick={() => void catalog.fetchNextPage()}
        >
          加载更多已发布事件
        </Button>
      )}
      <div className="analysis-query-footer">
        <div className="inline-actions">
          <Button onClick={() => setEditor('range')} aria-haspopup="dialog">
            时间范围
          </Button>
          <Button onClick={() => setEditor('filters')} aria-haspopup="dialog">
            过滤条件（{form.filters.length}）
          </Button>
          <Button onClick={() => setEditor('settings')} aria-haspopup="dialog">
            查询设置
          </Button>
        </div>
        <div className="inline-actions">
          {activeJob(snapshot?.job) && (
            <Button loading={canceling} onClick={() => void cancel()}>
              取消查询
            </Button>
          )}
          <Button
            type="submit"
            variant="primary"
            loading={submitting}
            disabled={busy || blockedDetails || catalog.isPending || catalog.isError}
          >
            运行查询
          </Button>
        </div>
      </div>
      <div className="analysis-query-summary" aria-label="当前查询条件">
        <span>
          <time dateTime={`${form.from}Z`}>{form.from.replace('T', ' ')}</time> 至{' '}
          <time dateTime={`${form.to}Z`}>{form.to.replace('T', ' ')}</time>（不含），UTC
        </span>
        <span>
          显示时区 {form.timezone}，最多 {form.limit || '—'} 行
          {isMetric && form.metric === 'uv'
            ? `，${form.accuracy === 'exact' ? '精确' : '近似'} UV`
            : ''}
        </span>
        {form.filters.length > 0 && <span>{filterSummary}</span>}
      </div>
      {blockedDetails && (
        <p role="status">
          事件明细和用户旅程需要项目编辑权限，当前可使用指标、趋势、漏斗和留存分析。
        </p>
      )}
    </form>
  );
  return (
    <>
      <PageHeading title="分析查询" />
      {!canRead ? (
        <EmptyPanel description="暂无查询权限，需要项目读取权限。" />
      ) : (
        <div className="section-stack">
          <Panel label="查询条件" className="analysis-workbench">
            <Tabs
              label="分析类型"
              value={form.mode}
              onChange={(value) => patch('mode', value as AnalysisMode)}
              items={modes.map((mode) => ({
                id: mode.id,
                label: mode.label,
                content: form.mode === mode.id ? queryEditor : null,
              }))}
            />
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
            <Panel label="查询结果" className="analysis-result">
              <ResultHeader
                title={`${snapshot.name}${snapshot.request.kind === 'trend' ? `，${metricLabel(snapshot.request, snapshot.semanticMetric)}` : ''}`}
                summary={
                  <span>
                    {snapshot.request.event_keys?.join('、') ||
                      (snapshot.request.kind === 'funnel'
                        ? `${snapshot.request.funnel?.steps.length ?? 0} 步有序漏斗`
                        : snapshot.request.kind === 'retention'
                          ? `${snapshot.request.retention?.periods ?? 0} 期${snapshot.request.retention?.granularity === 'week' ? '按周' : '按天'}留存`
                          : snapshot.request.kind === 'segment'
                            ? `按 ${snapshot.request.segment?.field.key ?? ''} 分群`
                            : snapshot.request.kind === 'path'
                              ? `深度 ${snapshot.request.path?.depth ?? 0} 事件路径`
                              : snapshot.request.kind === 'attribution'
                                ? `末次触点归因，窗口 ${snapshot.request.attribution?.window_seconds ?? 0} 秒`
                                : '全部事件')}
                    {snapshot.request.subject
                      ? `，${snapshot.request.subject === 'uid' ? '原始 UID' : '原始匿名标识'}`
                      : ''}
                  </span>
                }
                action={
                  <div className="inline-actions">
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
                    {result && (
                      <div className="inline-actions">
                        <Badge
                          tone={result.metadata.accuracy === 'approximate' ? 'warning' : 'neutral'}
                        >
                          {result.metadata.accuracy === 'exact'
                            ? '精确'
                            : result.metadata.accuracy === 'approximate'
                              ? '近似'
                              : '明细'}
                        </Badge>
                        <Badge
                          tone={
                            result.metadata.coverage.state === 'settled' ? 'success' : 'neutral'
                          }
                        >
                          {
                            { provisional: '覆盖暂定', settled: '覆盖已稳定', partial: '部分覆盖' }[
                              result.metadata.coverage.state
                            ]
                          }
                        </Badge>
                      </div>
                    )}
                  </div>
                }
              />
              <div className="section-stack">
                <div className="analysis-query-context">
                  <span>
                    {formatTime(snapshot.request.time_range.from, snapshot.request.timezone)} 至{' '}
                    {formatTime(snapshot.request.time_range.to, snapshot.request.timezone)}
                    （不含），{snapshot.request.timezone}
                  </span>
                  <span>
                    {snapshot.request.environment}，上限 {snapshot.request.limit} 行
                    {snapshot.request.interval
                      ? `，按${{ minute: '分钟', hour: '小时', day: '天' }[snapshot.request.interval]}`
                      : ''}
                    {snapshot.request.filter ? '，已应用过滤条件' : ''}
                  </span>
                </div>
                {dirty && (
                  <p className="analysis-stale-notice" role="status">
                    条件已修改，以下仍是上次运行的结果。运行查询后更新。
                  </p>
                )}
                {(notice || observing) && (
                  <div role="status" aria-live="polite">
                    {notice ||
                      (snapshot.job.state === 'succeeded'
                        ? '正在获取结果。'
                        : '按服务端间隔检查查询状态。')}
                  </div>
                )}
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
                    {snapshot.request.kind === 'trend' &&
                      !snapshot.request.interval &&
                      rows.length > 0 && (
                        <div className="analysis-stat">
                          <div>
                            <span>{metricLabel(snapshot.request, snapshot.semanticMetric)}</span>
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
                              : metricLabel(snapshot.request, snapshot.semanticMetric)
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
                    <details>
                      <summary>精度与覆盖详情</summary>
                      <dl className="detail-list">
                        <div>
                          <dt>精度</dt>
                          <dd>
                            {result.metadata.accuracy === 'exact'
                              ? '精确'
                              : result.metadata.accuracy === 'approximate'
                                ? '近似'
                                : '不适用'}
                            ，采样率 {(result.metadata.sample_rate * 100).toLocaleString('zh-CN')}%
                          </dd>
                        </div>
                        <div>
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
                        </div>
                        <div>
                          <dt>已知缺口</dt>
                          <dd>
                            {result.metadata.coverage.known_gaps.length
                              ? result.metadata.coverage.known_gaps
                                  .map(
                                    (gap) =>
                                      ({
                                        pipeline_completeness_not_established:
                                          '尚未确认接入链路完整性',
                                        identity_resolution_not_applied: '未应用身份关联',
                                      })[gap] ?? `未识别的覆盖提示（${gap}）`,
                                  )
                                  .join('、')
                              : '未报告'}
                          </dd>
                        </div>
                        <div>
                          <dt>实际观察到的最晚接收时间</dt>
                          <dd>
                            {result.metadata.data_watermark
                              ? formatTime(
                                  result.metadata.data_watermark,
                                  snapshot.request.timezone,
                                )
                              : '无观测数据'}
                            ，不代表数据已完整到达
                          </dd>
                        </div>
                        <div>
                          <dt>生成时间</dt>
                          <dd>
                            {formatTime(result.metadata.generated_at, snapshot.request.timezone)}，
                            {result.metadata.cache_hit ? '命中缓存' : '未命中缓存'}
                          </dd>
                        </div>
                      </dl>
                    </details>
                  </>
                )}
                <details>
                  <summary>查询标识与原始条件</summary>
                  <CopyButton value={snapshot.job.query_id} label="复制查询标识" />
                  <CodeBlock label="本次查询参数">
                    {JSON.stringify(snapshot.request, null, 2)}
                  </CodeBlock>
                </details>
              </div>
            </Panel>
          ) : (
            <EmptyPanel description="尚未运行查询，选择条件并运行，结果会显示本次查询的时间、环境与精度。" />
          )}
        </div>
      )}
      <Modal
        open={editor === 'range'}
        title="时间范围"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-range`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'range' && editor === 'range' && (
          <p id={`${queryFormID}-range-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-range`}
          aria-describedby={
            validationTarget === 'range' && validationError
              ? `${queryFormID}-range-error`
              : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('range');
          }}
        >
          <div className="inline-actions" role="group" aria-label="快捷时间范围">
            {[
              { label: '最近 1 小时', hours: 1 },
              { label: '最近 24 小时', hours: 24 },
              { label: '最近 7 天', hours: 168 },
            ].map((preset) => (
              <Button
                key={preset.hours}
                onClick={() => {
                  const end = new Date();
                  end.setUTCSeconds(0, 0);
                  setValidationError('');
                  setValidationTarget(undefined);
                  setForm((current) => ({
                    ...current,
                    from: new Date(end.getTime() - preset.hours * 3600000)
                      .toISOString()
                      .slice(0, 16),
                    to: end.toISOString().slice(0, 16),
                  }));
                }}
              >
                {preset.label}
              </Button>
            ))}
          </div>
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
          </div>
          <p className="field-help">
            时间范围为左闭右开，
            {form.mode === 'metrics' || form.mode === 'trend'
              ? '最多 31 天'
              : form.mode === 'retention'
                ? `最多 ${Number(form.retentionPeriods) * (form.retentionGranularity === 'week' ? 7 : 1)} 天`
                : '最多 7 天'}
            。输入采用 UTC，分桶与结果采用所选时区。
          </p>
        </form>
      </Modal>
      <Modal
        open={editor === 'filters'}
        title="过滤条件"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-filters`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'filters' && editor === 'filters' && (
          <p id={`${queryFormID}-filters-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-filters`}
          aria-describedby={
            validationTarget === 'filters' && validationError
              ? `${queryFormID}-filters-error`
              : undefined
          }
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('filters');
          }}
        >
          <div className="section-stack">
            <Toolbar
              actions={
                <Button
                  disabled={form.filters.length >= 20}
                  data-filter-add
                  onClick={(event) => {
                    requestRowFocus(
                      event.currentTarget,
                      `[data-filter-row="${form.filters.length}"] select`,
                    );
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
            />
            <div className="field-grid">
              <Field label="添加属性字段的事件">
                <Select
                  value={propertyEvent}
                  onChange={(event) => {
                    const key = event.target.value;
                    setPropertyEvent(key);
                    setPropertyVersion(
                      String(
                        events.find((item) => item.event_key === key)?.published_versions.at(-1) ??
                          '',
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
            {propertyEvent && version.isFetching && <LoadingPanel label="正在读取已发布属性类型" />}
            {propertyEvent && version.isError && (
              <ErrorPanel message={version.error.message} retry={() => void version.refetch()} />
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
                <div className="analysis-filter-row" data-filter-row={index} key={filter.id}>
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
                      options={fieldOperators(filter.field, filter.property?.type).map((op) => ({
                        value: op,
                        label: operators[op] ?? op,
                      }))}
                    />
                  </Field>
                  {filter.op !== 'is_missing' && filter.op !== 'is_present' && (
                    <Field
                      label={`条件 ${index + 1} 值${filter.op === 'in' || (filter.property?.type.startsWith('array:') && filter.op !== 'contains') ? '（JSON 数组，最多 20 项）' : filter.field === 'event_time' ? '（RFC3339，含时区）' : ''}`}
                    >
                      {filter.op === 'in' ||
                      (filter.property?.type.startsWith('array:') && filter.op !== 'contains') ? (
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
                    onClick={(event) => {
                      requestRowFocus(
                        event.currentTarget,
                        form.filters.length > 1
                          ? `[data-filter-row="${Math.min(index, form.filters.length - 2)}"] select`
                          : '[data-filter-add]',
                      );
                      patch(
                        'filters',
                        form.filters.filter((item) => item.id !== filter.id),
                      );
                    }}
                  >
                    删除
                  </Button>
                </div>
              ))}
            </div>
          </div>
        </form>
      </Modal>
      <Modal
        open={editor === 'settings'}
        title="查询设置"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-settings`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'settings' && editor === 'settings' && (
          <p id={`${queryFormID}-settings-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-settings`}
          aria-describedby={
            validationTarget === 'settings' && validationError
              ? `${queryFormID}-settings-error`
              : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('settings');
          }}
        >
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
          {isMetric && form.metric === 'uv' && (
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
          <p className="field-help">查询最多返回 500 行，超出预算会明确失败，结果不支持翻页。</p>
        </form>
      </Modal>
      <Modal
        open={editor === 'funnel'}
        title="配置有序漏斗"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-funnel`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'funnel' && editor === 'funnel' && (
          <p id={`${queryFormID}-funnel-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-funnel`}
          aria-describedby={
            validationTarget === 'funnel' && validationError
              ? `${queryFormID}-funnel-error`
              : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('funnel');
          }}
        >
          <div className="editor-list" aria-label="有序漏斗步骤">
            {form.steps.map((step, index) => (
              <div className="analysis-funnel-step" data-funnel-step={index} key={index}>
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
                                    .find((definition) => definition.event_key === eventKey)
                                    ?.published_versions.at(-1) ?? '',
                                ),
                              }
                            : item,
                        ),
                      );
                    }}
                    options={[{ value: '', label: '选择已发布事件' }, ...eventOptions.slice(1)]}
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
                          position === index ? { ...item, version: event.target.value } : item,
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
                    onClick={(event) => {
                      requestRowFocus(
                        event.currentTarget,
                        `[data-funnel-step="${Math.min(index, form.steps.length - 2)}"] select`,
                      );
                      patch(
                        'steps',
                        form.steps.filter((_, position) => position !== index),
                      );
                    }}
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
                onClick={(event) => {
                  requestRowFocus(
                    event.currentTarget,
                    `[data-funnel-step="${form.steps.length}"] select`,
                  );
                  patch('steps', [...form.steps, { event: '', version: '' }]);
                }}
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
        </form>
      </Modal>
      <Modal
        open={editor === 'retention'}
        title="配置留存分析"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-retention`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'retention' && editor === 'retention' && (
          <p id={`${queryFormID}-retention-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-retention`}
          aria-describedby={
            validationTarget === 'retention' && validationError
              ? `${queryFormID}-retention-error`
              : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('retention');
          }}
        >
          {(
            [
              { key: 'retentionStart', label: '起始事件' },
              { key: 'retentionReturn', label: '回访事件' },
            ] as const
          ).map(({ key, label }) => (
            <div className="analysis-funnel-step" data-retention-step={key} key={key}>
              <Field label={`${label}（已发布事件）`}>
                <Select
                  required
                  value={form[key].event}
                  onChange={(event) => {
                    const eventKey = event.target.value;
                    patch(key, {
                      event: eventKey,
                      version: String(
                        events
                          .find((definition) => definition.event_key === eventKey)
                          ?.published_versions.at(-1) ?? '',
                      ),
                    });
                  }}
                  options={[{ value: '', label: '选择已发布事件' }, ...eventOptions.slice(1)]}
                />
              </Field>
              <Field label={`${label}版本`}>
                <Select
                  required
                  value={form[key].version}
                  onChange={(event) => patch(key, { ...form[key], version: event.target.value })}
                  options={[
                    { value: '', label: '选择版本' },
                    ...(
                      events.find((event) => event.event_key === form[key].event)
                        ?.published_versions ?? []
                    ).map((version) => ({ value: String(version), label: `v${version}` })),
                  ]}
                />
              </Field>
            </div>
          ))}
          <Field label="留存粒度">
            <Select
              value={form.retentionGranularity}
              onChange={(event) =>
                patch(
                  'retentionGranularity',
                  event.target.value as AnalysisForm['retentionGranularity'],
                )
              }
              options={[
                { value: 'day', label: '按天（时区对齐日历日）' },
                { value: 'week', label: '按周（周一开始）' },
              ]}
            />
          </Field>
          <Field label="留存期数（1 至 12）">
            <Input
              type="number"
              required
              min="1"
              max="12"
              value={form.retentionPeriods}
              onChange={(event) => patch('retentionPeriods', event.target.value)}
            />
          </Field>
          <p className="field-help">
            主体在最早起始事件所在周期入队，回访事件按相对队列的周期偏移计数，0 期留存恒为
            100%。统计主体在查询栏的「统计主体」中选择原始 UID 或原始匿名标识。
          </p>
        </form>
      </Modal>
      <Modal
        open={editor === 'segment'}
        title="配置主体分群"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-segment`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'segment' && editor === 'segment' && (
          <p id={`${queryFormID}-segment-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-segment`}
          aria-describedby={
            validationTarget === 'segment' && validationError
              ? `${queryFormID}-segment-error`
              : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('segment');
          }}
        >
          <div className="analysis-funnel-step" data-segment-config>
            <Field label="分群事件（已发布）">
              <Select
                required
                value={form.segmentEvent.event}
                onChange={(event) => {
                  const eventKey = event.target.value;
                  const version = String(
                    events
                      .find((definition) => definition.event_key === eventKey)
                      ?.published_versions.at(-1) ?? '',
                  );
                  patch('segmentEvent', { event: eventKey, version });
                  patch('segmentField', '');
                  patch('segmentProperty', undefined);
                  setPropertyEvent(eventKey);
                  setPropertyVersion(version);
                }}
                options={[{ value: '', label: '选择已发布事件' }, ...eventOptions.slice(1)]}
              />
            </Field>
            <Field label="分群事件版本">
              <Select
                required
                value={form.segmentEvent.version}
                onChange={(event) => {
                  const version = event.target.value;
                  patch('segmentEvent', { ...form.segmentEvent, version });
                  patch('segmentField', '');
                  patch('segmentProperty', undefined);
                  setPropertyVersion(version);
                }}
                options={[
                  { value: '', label: '选择版本' },
                  ...(
                    events.find((event) => event.event_key === form.segmentEvent.event)
                      ?.published_versions ?? []
                  ).map((version) => ({ value: String(version), label: `v${version}` })),
                ]}
              />
            </Field>
          </div>
          {form.segmentEvent.event && version.isFetching && (
            <LoadingPanel label="正在读取已发布属性类型" />
          )}
          {form.segmentEvent.event && version.isError && (
            <ErrorPanel message={version.error.message} retry={() => void version.refetch()} />
          )}
          {form.segmentEvent.event && version.data && (
            <Field label="分群标量字段">
              <Select
                required
                value={form.segmentField}
                onChange={(event) => {
                  const property = scalarSegmentFields.find(
                    (field) => field.id === event.target.value,
                  );
                  patch('segmentField', event.target.value);
                  patch('segmentProperty', property);
                }}
                options={[
                  { value: '', label: '选择标量属性字段' },
                  ...scalarSegmentFields.map((field) => ({
                    value: field.id,
                    label: field.label,
                  })),
                ]}
              />
            </Field>
          )}
          {form.segmentEvent.event && version.data && !scalarSegmentFields.length && (
            <p className="field-help">此版本没有可用于分群的标量属性字段。</p>
          )}
          <p className="field-help">
            每个主体按最早一条分群事件的该字段值归入唯一群体；缺失或空值归入固定缺失组。不支持数组与日期时间字段。
          </p>
        </form>
      </Modal>
      <Modal
        open={editor === 'path'}
        title="配置聚合路径"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-path`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'path' && editor === 'path' && (
          <p id={`${queryFormID}-path-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-path`}
          aria-describedby={
            validationTarget === 'path' && validationError ? `${queryFormID}-path-error` : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('path');
          }}
        >
          <div className="analysis-funnel-step" data-path-config>
            <Field label="起始事件（可选，已发布事件）">
              <Select
                value={form.pathStart.event}
                onChange={(event) => {
                  const eventKey = event.target.value;
                  patch('pathStart', {
                    event: eventKey,
                    version: String(
                      events
                        .find((definition) => definition.event_key === eventKey)
                        ?.published_versions.at(-1) ?? '',
                    ),
                  });
                }}
                options={[
                  { value: '', label: '不指定（从窗口内最早事件开始）' },
                  ...eventOptions.slice(1),
                ]}
              />
            </Field>
            {form.pathStart.event && (
              <Field label="起始事件版本">
                <Select
                  required
                  value={form.pathStart.version}
                  onChange={(event) =>
                    patch('pathStart', { ...form.pathStart, version: event.target.value })
                  }
                  options={[
                    { value: '', label: '选择版本' },
                    ...(
                      events.find((event) => event.event_key === form.pathStart.event)
                        ?.published_versions ?? []
                    ).map((version) => ({ value: String(version), label: `v${version}` })),
                  ]}
                />
              </Field>
            )}
          </div>
          <Field label="路径深度（2 至 8 个事件）">
            <Input
              type="number"
              required
              min="2"
              max="8"
              value={form.pathDepth}
              onChange={(event) => patch('pathDepth', event.target.value)}
            />
          </Field>
          <p className="field-help">
            指定起始事件时，从每个主体最早匹配事件起取后续深度个事件；不指定时从窗口内最早事件起取。事件数不足深度的主体不计入，每个主体最多贡献一条路径。统计主体在查询栏选择原始
            UID 或原始匿名标识。
          </p>
        </form>
      </Modal>
      <Modal
        open={editor === 'attribution'}
        title="配置归因分析"
        onRequestClose={() => setEditor(null)}
        footer={
          <Button variant="primary" type="submit" form={`${queryFormID}-attribution`}>
            完成
          </Button>
        }
      >
        {validationError && validationTarget === 'attribution' && editor === 'attribution' && (
          <p id={`${queryFormID}-attribution-error`} role="alert">
            {validationError}
          </p>
        )}
        <form
          id={`${queryFormID}-attribution`}
          aria-describedby={
            validationTarget === 'attribution' && validationError
              ? `${queryFormID}-attribution-error`
              : undefined
          }
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            finishEditor('attribution');
          }}
        >
          <div className="analysis-funnel-step" data-attribution-conversion>
            <Field label="转化事件（已发布）">
              <Select
                required
                value={form.attributionConversion.event}
                onChange={(event) => {
                  const eventKey = event.target.value;
                  patch('attributionConversion', {
                    event: eventKey,
                    version: String(
                      events
                        .find((definition) => definition.event_key === eventKey)
                        ?.published_versions.at(-1) ?? '',
                    ),
                  });
                }}
                options={[{ value: '', label: '选择已发布事件' }, ...eventOptions.slice(1)]}
              />
            </Field>
            <Field label="转化事件版本">
              <Select
                required
                value={form.attributionConversion.version}
                onChange={(event) =>
                  patch('attributionConversion', {
                    ...form.attributionConversion,
                    version: event.target.value,
                  })
                }
                options={[
                  { value: '', label: '选择版本' },
                  ...(
                    events.find((event) => event.event_key === form.attributionConversion.event)
                      ?.published_versions ?? []
                  ).map((version) => ({ value: String(version), label: `v${version}` })),
                ]}
              />
            </Field>
          </div>
          <div className="editor-list" aria-label="归因触点">
            {form.attributionTouchpoints.map((step, index) => (
              <div className="analysis-funnel-step" data-attribution-touchpoint={index} key={index}>
                <span>触点 {index + 1}</span>
                <Field label={`触点 ${index + 1} 事件`}>
                  <Select
                    required
                    value={step.event}
                    onChange={(event) => {
                      const eventKey = event.target.value;
                      patch(
                        'attributionTouchpoints',
                        form.attributionTouchpoints.map((item, position) =>
                          position === index
                            ? {
                                event: eventKey,
                                version: String(
                                  events
                                    .find((definition) => definition.event_key === eventKey)
                                    ?.published_versions.at(-1) ?? '',
                                ),
                              }
                            : item,
                        ),
                      );
                    }}
                    options={[{ value: '', label: '选择已发布事件' }, ...eventOptions.slice(1)]}
                  />
                </Field>
                <Field label={`触点 ${index + 1} 版本`}>
                  <Select
                    required
                    value={step.version}
                    onChange={(event) =>
                      patch(
                        'attributionTouchpoints',
                        form.attributionTouchpoints.map((item, position) =>
                          position === index ? { ...item, version: event.target.value } : item,
                        ),
                      )
                    }
                    options={[
                      { value: '', label: '选择版本' },
                      ...(
                        events.find((event) => event.event_key === step.event)
                          ?.published_versions ?? []
                      ).map((version) => ({ value: String(version), label: `v${version}` })),
                    ]}
                  />
                </Field>
                <div className="editor-row-actions">
                  <Button
                    size="small"
                    aria-label={`删除触点 ${index + 1}`}
                    disabled={form.attributionTouchpoints.length <= 1}
                    onClick={() =>
                      patch(
                        'attributionTouchpoints',
                        form.attributionTouchpoints.filter((_, position) => position !== index),
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
                disabled={form.attributionTouchpoints.length >= 8}
                onClick={() =>
                  patch('attributionTouchpoints', [
                    ...form.attributionTouchpoints,
                    { event: '', version: '' },
                  ])
                }
              >
                添加触点
              </Button>
            }
          >
            <Field label="归因回溯窗口（秒，1 至 604800）">
              <Input
                type="number"
                required
                min="1"
                max="604800"
                value={form.attributionWindow}
                onChange={(event) => patch('attributionWindow', event.target.value)}
              />
            </Field>
          </Toolbar>
          <p className="field-help">
            触点 1–8 个且不能与转化事件相同、彼此不能重复；v1
            仅支持末次触点（last_touch）模型。统计主体在查询栏选择原始 UID 或原始匿名标识。
          </p>
        </form>
      </Modal>
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
