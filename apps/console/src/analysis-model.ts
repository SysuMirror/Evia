import type { Environment, QueryRequest } from '@evia/api-client';

export type AnalysisMode = 'metrics' | 'trend' | 'events' | 'journey' | 'funnel' | 'retention';
export type QueryField = NonNullable<QueryRequest['group_by']>[number];
export type QueryFilter = NonNullable<QueryRequest['filter']>;
export type PublishedField = { id: string; label: string; type: string; reference: QueryField };
export type FilterRow = {
  id: number;
  field: string;
  op: string;
  value: string;
  property?: PublishedField;
};
export type AnalysisForm = {
  mode: AnalysisMode;
  from: string;
  to: string;
  timezone: string;
  event: string;
  metric: 'count' | 'pv' | 'uv';
  subject: 'uid' | 'anonymous';
  accuracy: 'exact' | 'approximate';
  interval: 'minute' | 'hour' | 'day';
  identity: string;
  limit: string;
  filters: FilterRow[];
  filterJoin: 'and' | 'or';
  steps: { event: string; version: string }[];
  window: string;
  retentionStart: { event: string; version: string };
  retentionReturn: { event: string; version: string };
  retentionGranularity: 'day' | 'week';
  retentionPeriods: string;
};
export const modes: { id: AnalysisMode; label: string }[] = [
  { id: 'metrics', label: '指标' },
  { id: 'trend', label: '趋势' },
  { id: 'events', label: '事件明细' },
  { id: 'journey', label: '用户旅程' },
  { id: 'funnel', label: '有序漏斗' },
  { id: 'retention', label: '留存分析' },
];
export const systemFields = [
  { key: 'event_key', label: '事件标识', type: 'string' },
  { key: 'uid', label: '原始 UID', type: 'string' },
  { key: 'anonymous_id', label: '匿名标识', type: 'string' },
  { key: 'session_id', label: '会话标识', type: 'string' },
  { key: 'page_view_id', label: '页面访问标识', type: 'string' },
  { key: 'event_time', label: '事件时间', type: 'datetime' },
  { key: 'operation_id', label: '操作标识', type: 'string' },
] as const;
export const operators: Record<string, string> = {
  eq: '等于',
  neq: '不等于',
  in: '属于',
  gt: '大于',
  gte: '大于等于',
  lt: '小于',
  lte: '小于等于',
  prefix: '开头是',
  contains: '包含',
  is_present: '存在',
  is_missing: '缺失',
};
export function fieldOperators(key: string, type?: string) {
  const kind = type ?? (key === 'event_time' ? 'datetime' : 'string');
  if (kind.startsWith('array:')) return ['eq', 'neq', 'contains', 'is_present', 'is_missing'];
  if (kind === 'boolean') return ['eq', 'neq', 'in', 'is_present', 'is_missing'];
  if (kind === 'number' || kind === 'integer' || kind === 'datetime')
    return ['eq', 'neq', 'in', 'gt', 'gte', 'lt', 'lte', 'is_present', 'is_missing'];
  return ['eq', 'neq', 'in', 'prefix', 'contains', 'is_present', 'is_missing'];
}
export function publishedFields(
  schema: Record<string, unknown>,
  event: string,
  version: number,
): PublishedField[] {
  const properties = schema.properties;
  if (!properties || typeof properties !== 'object' || Array.isArray(properties)) return [];
  return Object.entries(properties).flatMap(([key, value]) => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return [];
    const definition = value as Record<string, unknown>;
    let type = String(definition.type);
    if (type === 'array') {
      const items = definition.items;
      if (!items || typeof items !== 'object' || Array.isArray(items)) return [];
      type = `array:${String((items as Record<string, unknown>).type)}`;
    }
    if (
      ![
        'string',
        'boolean',
        'number',
        'integer',
        'array:string',
        'array:boolean',
        'array:number',
        'array:integer',
      ].includes(type)
    )
      return [];
    return [
      {
        id: `${event}:${version}:${key}`,
        label: `${event} v${version} / ${key} (${type})`,
        type,
        reference: { kind: 'property' as const, event_key: event, schema_version: version, key },
      },
    ];
  });
}
export function initialForm(timezone: string): AnalysisForm {
  const end = new Date();
  end.setUTCSeconds(0, 0);
  return {
    mode: 'metrics',
    from: new Date(end.getTime() - 86400000).toISOString().slice(0, 16),
    to: end.toISOString().slice(0, 16),
    timezone,
    event: '',
    metric: 'count',
    subject: 'uid',
    accuracy: 'exact',
    interval: 'hour',
    identity: '',
    limit: '200',
    filters: [],
    filterJoin: 'and',
    steps: [
      { event: '', version: '' },
      { event: '', version: '' },
    ],
    window: '3600',
    retentionStart: { event: '', version: '' },
    retentionReturn: { event: '', version: '' },
    retentionGranularity: 'day',
    retentionPeriods: '7',
  };
}
export type AnalysisValidationTarget =
  'range' | 'settings' | 'event' | 'interval' | 'filters' | 'identity' | 'funnel' | 'retention';
export class AnalysisValidationError extends Error {
  constructor(
    public readonly target: AnalysisValidationTarget,
    message: string,
  ) {
    super(message);
    this.name = 'AnalysisValidationError';
  }
}

function readTimeRange(form: AnalysisForm) {
  const from = new Date(`${form.from}Z`),
    to = new Date(`${form.to}Z`);
  const days =
    form.mode === 'metrics' || form.mode === 'trend'
      ? 31
      : form.mode === 'retention'
        ? retentionSpanDays(form)
        : 7;
  if (
    !Number.isFinite(from.getTime()) ||
    !Number.isFinite(to.getTime()) ||
    from >= to ||
    to.getTime() - from.getTime() > days * 86400000
  )
    throw new AnalysisValidationError(
      'range',
      `请选择有效的时间范围，开始早于结束，最多 ${days} 天。`,
    );
  if (from.getUTCFullYear() < 1970 || to.getUTCFullYear() > 2100)
    throw new AnalysisValidationError('range', '查询时间须在 1970 至 2100 年之间。');
  try {
    new Intl.DateTimeFormat('zh-CN', { timeZone: form.timezone });
  } catch {
    throw new AnalysisValidationError('range', '请选择有效的 IANA 时区。');
  }
  return { from, to };
}

// retentionSpanDays mirrors the backend window: periods multiplied by the
// calendar granularity, with an 84-day hard ceiling.
function retentionSpanDays(form: AnalysisForm): number {
  const periods = Number(form.retentionPeriods);
  const safePeriods = Number.isInteger(periods) && periods >= 1 && periods <= 12 ? periods : 12;
  return safePeriods * (form.retentionGranularity === 'week' ? 7 : 1);
}
function readRetentionStep(
  step: { event: string; version: string },
  label: string,
): NonNullable<QueryRequest['retention']>['start_event'] {
  if (!step.event || !Number.isSafeInteger(Number(step.version)) || Number(step.version) < 1)
    throw new AnalysisValidationError('retention', `请为${label}选择已发布事件及版本。`);
  return { event_key: step.event, schema_version: Number(step.version) };
}
function readRetention(form: AnalysisForm): NonNullable<QueryRequest['retention']> {
  if (form.retentionGranularity !== 'day' && form.retentionGranularity !== 'week')
    throw new AnalysisValidationError('retention', '留存粒度须为按天或按周。');
  const periods = Number(form.retentionPeriods);
  if (!Number.isInteger(periods) || periods < 1 || periods > 12)
    throw new AnalysisValidationError('retention', '留存期数须为 1 至 12 的整数。');
  return {
    start_event: readRetentionStep(form.retentionStart, '起始事件'),
    return_event: readRetentionStep(form.retentionReturn, '回访事件'),
    granularity: form.retentionGranularity,
    periods,
  };
}
function readLimit(form: AnalysisForm) {
  const limit = Number(form.limit);
  if (!Number.isInteger(limit) || limit < 1 || limit > 500)
    throw new AnalysisValidationError('settings', '结果行数须为 1 至 500 的整数。');
  return limit;
}
function readFilters(form: AnalysisForm): QueryFilter[] {
  return form.filters.map((row, index) => {
    const system = systemFields.find((item) => item.key === row.field);
    const field =
      row.property ??
      (system
        ? { type: system.type, reference: { kind: 'system' as const, key: system.key } }
        : undefined);
    if (!field || !fieldOperators(row.field, field.type).includes(row.op))
      throw new AnalysisValidationError('filters', `第 ${index + 1} 条过滤条件无效。`);
    const reference: QueryField = field.reference;
    if (row.op === 'is_missing' || row.op === 'is_present') return { op: row.op, field: reference };
    const arrayValue =
      row.op === 'in' || (field.type.startsWith('array:') && row.op !== 'contains');
    let values: unknown[];
    if (arrayValue) {
      let value: unknown;
      try {
        value = JSON.parse(row.value);
      } catch {
        throw new AnalysisValidationError(
          'filters',
          `第 ${index + 1} 条过滤值须为 JSON 数组，例如 [] 或 ["value"]。`,
        );
      }
      if (!Array.isArray(value))
        throw new AnalysisValidationError('filters', `第 ${index + 1} 条过滤值须为 JSON 数组。`);
      values = value;
    } else values = [row.value];
    if (values.length > 20)
      throw new AnalysisValidationError('filters', `第 ${index + 1} 条过滤值最多包含 20 项。`);
    const kind = field.type.replace('array:', '');
    const parsed = values.map((item): string | number | boolean => {
      if (kind === 'boolean') {
        if (arrayValue && typeof item === 'boolean') return item;
        if (arrayValue || (item !== 'true' && item !== 'false'))
          throw new AnalysisValidationError('filters', `第 ${index + 1} 条过滤值须为布尔类型。`);
        return item === 'true';
      }
      if (kind === 'number' || kind === 'integer') {
        if (arrayValue ? typeof item !== 'number' : typeof item !== 'string' || !item.trim())
          throw new AnalysisValidationError('filters', `第 ${index + 1} 条过滤值须为数值类型。`);
        const number = Number(item);
        if (
          !Number.isFinite(number) ||
          Math.abs(number) > Number.MAX_SAFE_INTEGER ||
          (kind === 'integer' && !Number.isSafeInteger(number))
        )
          throw new AnalysisValidationError(
            'filters',
            `第 ${index + 1} 条过滤值须为有效安全${kind === 'integer' ? '整数' : '数值'}。`,
          );
        return number;
      }
      if (typeof item !== 'string' || [...item].length > 1024)
        throw new AnalysisValidationError(
          'filters',
          `第 ${index + 1} 条过滤值须为最多 1024 字的字符串。`,
        );
      if (
        kind === 'datetime' &&
        (!/^\d{4}-\d\d-\d\dT.*(?:Z|[+-]\d\d:\d\d)$/.test(item) ||
          !Number.isFinite(Date.parse(item)))
      )
        throw new AnalysisValidationError(
          'filters',
          '时间过滤值须包含时区，例如 2026-01-01T00:00:00Z。',
        );
      return item;
    });
    return { op: row.op as 'eq', field: reference, value: arrayValue ? parsed : parsed[0]! };
  });
}
function readFunnel(form: AnalysisForm): NonNullable<QueryRequest['funnel']> {
  if (form.steps.length < 2 || form.steps.length > 8)
    throw new AnalysisValidationError('funnel', '漏斗须包含 2 至 8 个有序步骤。');
  const window = Number(form.window);
  if (!Number.isInteger(window) || window < 1 || window > 604800)
    throw new AnalysisValidationError('funnel', '转化窗口须为 1 至 604800 秒。');
  if (
    form.steps.some(
      (step) =>
        !step.event || !Number.isSafeInteger(Number(step.version)) || Number(step.version) < 1,
    )
  )
    throw new AnalysisValidationError('funnel', '请为每个漏斗步骤选择已发布事件及版本。');
  return {
    steps: form.steps.map((step) => ({
      event_key: step.event,
      schema_version: Number(step.version),
    })),
    window_seconds: window,
    mode: 'ordered_with_interleaving',
  };
}
export type AnalysisEditorSection = 'range' | 'settings' | 'filters' | 'funnel' | 'retention';
export function validateAnalysisSection(form: AnalysisForm, section: AnalysisEditorSection): void {
  if (section === 'range') readTimeRange(form);
  else if (section === 'settings') readLimit(form);
  else if (section === 'filters') readFilters(form);
  else if (section === 'funnel') readFunnel(form);
  else readRetention(form);
}

export function buildRequest(form: AnalysisForm, environment: Environment): QueryRequest {
  const { from, to } = readTimeRange(form);
  const limit = readLimit(form);
  const request: QueryRequest = {
    kind: form.mode === 'metrics' ? 'trend' : form.mode,
    environment,
    time_range: { from: from.toISOString(), to: to.toISOString() },
    timezone: form.timezone,
    limit,
  };
  if (form.mode !== 'funnel' && form.mode !== 'retention' && form.event)
    request.event_keys = [form.event];
  if (form.mode === 'metrics' || form.mode === 'trend') {
    if (form.metric === 'pv' && !form.event)
      throw new AnalysisValidationError('event', '请选择代表页面访问的已发布事件，再统计 PV。');
    request.metrics = [
      {
        op: form.metric === 'uv' ? 'unique' : 'count',
        ...(form.metric === 'uv' ? { accuracy: form.accuracy } : {}),
      },
    ];
    if (form.metric === 'uv') request.subject = form.subject;
    if (form.mode === 'trend') request.interval = form.interval;
    // Do not submit a query that must exceed the bounded result contract.
    const size = { minute: 60000, hour: 3600000, day: 86400000 }[form.interval];
    if (form.mode === 'trend' && Math.ceil((to.getTime() - from.getTime()) / size) + 1 > limit)
      throw new AnalysisValidationError(
        'interval',
        '当前粒度可能超出结果行数，请增加行数、缩短时间或选择更大粒度。',
      );
  }
  const args = readFilters(form);
  if (args.length) request.filter = { op: form.filterJoin, args };
  if (form.mode === 'journey') {
    if (!form.identity.trim())
      throw new AnalysisValidationError('identity', '请输入原始身份标识。');
    if (
      form.subject === 'anonymous' &&
      !/^[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$/i.test(form.identity)
    )
      throw new AnalysisValidationError('identity', '匿名标识须为有效 UUID。');
    if (form.subject === 'uid' && [...form.identity].length > 128)
      throw new AnalysisValidationError('identity', 'UID 最多 128 字。');
    request.subject = form.subject;
    request.identity_filter =
      form.subject === 'uid' ? { uid: form.identity } : { anonymous_id: form.identity };
  }
  if (form.mode === 'funnel') {
    request.subject = form.subject;
    request.funnel = readFunnel(form);
  }
  if (form.mode === 'retention') {
    // v1 retention is uid-only; the backend rejects every other subject.
    request.subject = 'uid';
    request.retention = readRetention(form);
  }
  return request;
}
export function metricLabel(request: QueryRequest, semanticMetric?: AnalysisForm['metric']) {
  const metric = request.metrics?.[0];
  if (metric?.op === 'unique')
    return `${request.subject === 'anonymous' ? '匿名标识' : '原始 UID'} UV`;
  return semanticMetric === 'pv' ? 'PV' : '事件数';
}
export function formatTime(value: unknown, timezone: string) {
  if (typeof value !== 'string') return '—';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(date);
}
