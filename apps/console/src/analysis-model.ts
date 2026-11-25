import type { Environment, QueryRequest } from '@evia/api-client';

export type AnalysisMode =
  | 'metrics'
  | 'trend'
  | 'events'
  | 'journey'
  | 'funnel'
  | 'retention'
  | 'segment'
  | 'path'
  | 'attribution'
  | 'heatmap';
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
  interval: 'minute' | 'hour' | 'day' | 'week';
  identity: string;
  limit: string;
  filters: FilterRow[];
  filterJoin: 'and' | 'or';
  steps: { event: string; version: string }[];
  window: string;
  retentionStart: { event: string; version: string };
  retentionReturn: { event: string; version: string };
  retentionGranularity: 'day' | 'week' | 'month';
  retentionPeriods: string;
  segmentEvent: { event: string; version: string };
  segmentField: string;
  segmentProperty?: PublishedField;
  pathStart: { event: string; version: string };
  pathDepth: string;
  pathCollapseRepeats: boolean;
  attributionConversion: { event: string; version: string };
  attributionTouchpoints: { event: string; version: string }[];
  attributionWindow: string;
  attributionModel: 'last_touch' | 'first_touch' | 'linear' | 'position_based' | 'time_decay';
  attributionHalfLife: string;
  heatmapRouteField: 'route_name' | 'route_template';
  heatmapRoute: string;
  heatmapMode: 'grid' | 'elements';
  heatmapViewportScope: 'exact' | 'bucket' | 'all';
  heatmapViewportBucket: 'mobile' | 'tablet' | 'desktop';
  heatmapViewportWidth: string;
  heatmapViewportHeight: string;
};
export const modes: { id: AnalysisMode; label: string }[] = [
  { id: 'metrics', label: '指标' },
  { id: 'trend', label: '趋势' },
  { id: 'events', label: '事件明细' },
  { id: 'journey', label: '用户旅程' },
  { id: 'funnel', label: '有序漏斗' },
  { id: 'retention', label: '留存分析' },
  { id: 'segment', label: '主体分群' },
  { id: 'path', label: '聚合路径' },
  { id: 'attribution', label: '归因分析' },
  { id: 'heatmap', label: '视口点击分布' },
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
  not_in: '不属于',
  gt: '大于',
  gte: '大于等于',
  lt: '小于',
  lte: '小于等于',
  prefix: '开头是',
  not_prefix: '开头不是',
  suffix: '结尾是',
  not_suffix: '结尾不是',
  contains: '包含',
  not_contains: '不包含',
  is_present: '存在',
  is_missing: '缺失',
};
export function fieldOperators(key: string, type?: string) {
  const kind = type ?? (key === 'event_time' ? 'datetime' : 'string');
  if (kind.startsWith('array:'))
    return ['eq', 'neq', 'contains', 'not_contains', 'is_present', 'is_missing'];
  if (kind === 'boolean') return ['eq', 'neq', 'in', 'not_in', 'is_present', 'is_missing'];
  if (kind === 'number' || kind === 'integer' || kind === 'datetime')
    return ['eq', 'neq', 'in', 'not_in', 'gt', 'gte', 'lt', 'lte', 'is_present', 'is_missing'];
  return [
    'eq',
    'neq',
    'in',
    'not_in',
    'prefix',
    'not_prefix',
    'suffix',
    'not_suffix',
    'contains',
    'not_contains',
    'is_present',
    'is_missing',
  ];
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
    segmentEvent: { event: '', version: '' },
    segmentField: '',
    pathStart: { event: '', version: '' },
    pathDepth: '3',
    pathCollapseRepeats: false,
    attributionConversion: { event: '', version: '' },
    attributionTouchpoints: [{ event: '', version: '' }],
    attributionWindow: '86400',
    attributionModel: 'last_touch',
    attributionHalfLife: '600',
    heatmapRouteField: 'route_template',
    heatmapRoute: '',
    heatmapMode: 'grid',
    heatmapViewportScope: 'exact',
    heatmapViewportBucket: 'desktop',
    heatmapViewportWidth: '1366',
    heatmapViewportHeight: '900',
  };
}
export type AnalysisValidationTarget =
  | 'range'
  | 'settings'
  | 'event'
  | 'interval'
  | 'filters'
  | 'identity'
  | 'funnel'
  | 'retention'
  | 'segment'
  | 'path'
  | 'attribution'
  | 'heatmap';
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

// retentionSpanDays mirrors the backend scan window: day/week use their exact
// period length, while month uses the conservative 31-day upper bound (the
// evaluator aligns cohorts to calendar months independently of this duration).
function retentionSpanDays(form: AnalysisForm): number {
  const periods = Number(form.retentionPeriods);
  const safePeriods = Number.isInteger(periods) && periods >= 1 && periods <= 12 ? periods : 12;
  const daysPerPeriod =
    form.retentionGranularity === 'week' ? 7 : form.retentionGranularity === 'month' ? 31 : 1;
  return safePeriods * daysPerPeriod;
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
  if (
    form.retentionGranularity !== 'day' &&
    form.retentionGranularity !== 'week' &&
    form.retentionGranularity !== 'month'
  )
    throw new AnalysisValidationError('retention', '留存粒度须为按天、按周或按月。');
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
  const gridHeatmap = form.mode === 'heatmap' && form.heatmapMode !== 'elements';
  const minimum = gridHeatmap ? 200 : 1;
  if (!Number.isInteger(limit) || limit < minimum || limit > 500)
    throw new AnalysisValidationError(
      'settings',
      gridHeatmap ? '点击热力图结果行数须为 200 至 500 的整数。' : '结果行数须为 1 至 500 的整数。',
    );
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
      row.op === 'in' ||
      row.op === 'not_in' ||
      (field.type.startsWith('array:') && row.op !== 'contains' && row.op !== 'not_contains');
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
const scalarSegmentTypes = ['string', 'boolean', 'number', 'integer'];
function readSegment(form: AnalysisForm): NonNullable<QueryRequest['segment']> {
  if (
    !form.segmentEvent.event ||
    !Number.isSafeInteger(Number(form.segmentEvent.version)) ||
    Number(form.segmentEvent.version) < 1
  )
    throw new AnalysisValidationError('segment', '请为分群选择已发布事件及版本。');
  const property = form.segmentProperty;
  const reference = property?.reference;
  if (
    !property ||
    !reference ||
    reference.kind !== 'property' ||
    reference.event_key !== form.segmentEvent.event ||
    reference.schema_version !== Number(form.segmentEvent.version)
  )
    throw new AnalysisValidationError('segment', '请选择该事件版本上的标量属性字段。');
  if (!scalarSegmentTypes.includes(property.type))
    throw new AnalysisValidationError('segment', '分群字段必须是字符串、布尔或数值标量。');
  return {
    event: {
      event_key: form.segmentEvent.event,
      schema_version: Number(form.segmentEvent.version),
    },
    field: {
      kind: 'property',
      event_key: reference.event_key,
      schema_version: reference.schema_version,
      key: reference.key,
    },
  };
}
function readPath(form: AnalysisForm): NonNullable<QueryRequest['path']> {
  const depth = Number(form.pathDepth);
  if (!Number.isInteger(depth) || depth < 2 || depth > 8)
    throw new AnalysisValidationError('path', '路径深度须为 2 至 8 的整数。');
  const path: NonNullable<QueryRequest['path']> = { depth };
  if (form.pathCollapseRepeats) path.collapse_repeats = true;
  if (form.pathStart.event) {
    if (!Number.isSafeInteger(Number(form.pathStart.version)) || Number(form.pathStart.version) < 1)
      throw new AnalysisValidationError('path', '请为起始事件选择已发布版本。');
    path.start_event = {
      event_key: form.pathStart.event,
      schema_version: Number(form.pathStart.version),
    };
  }
  return path;
}
function readAttribution(form: AnalysisForm): NonNullable<QueryRequest['attribution']> {
  if (
    !form.attributionConversion.event ||
    !Number.isSafeInteger(Number(form.attributionConversion.version)) ||
    Number(form.attributionConversion.version) < 1
  )
    throw new AnalysisValidationError('attribution', '请为转化选择已发布事件及版本。');
  const touchpoints = form.attributionTouchpoints;
  if (touchpoints.length < 1 || touchpoints.length > 8)
    throw new AnalysisValidationError('attribution', '归因触点须为 1 至 8 个。');
  if (
    touchpoints.some(
      (step) =>
        !step.event || !Number.isSafeInteger(Number(step.version)) || Number(step.version) < 1,
    )
  )
    throw new AnalysisValidationError('attribution', '请为每个触点选择已发布事件及版本。');
  const conversionPair = `${form.attributionConversion.event}@${form.attributionConversion.version}`;
  const seen = new Set<string>();
  for (const step of touchpoints) {
    const pair = `${step.event}@${step.version}`;
    if (pair === conversionPair)
      throw new AnalysisValidationError('attribution', '触点不能与转化事件相同。');
    if (seen.has(pair)) throw new AnalysisValidationError('attribution', '触点事件不能重复。');
    seen.add(pair);
  }
  const window = Number(form.attributionWindow);
  if (!Number.isInteger(window) || window < 1 || window > 604800)
    throw new AnalysisValidationError('attribution', '归因窗口须为 1 至 604800 秒的整数。');
  const attribution: NonNullable<QueryRequest['attribution']> = {
    conversion: {
      event_key: form.attributionConversion.event,
      schema_version: Number(form.attributionConversion.version),
    },
    touchpoints: touchpoints.map((step) => ({
      event_key: step.event,
      schema_version: Number(step.version),
    })),
    window_seconds: window,
    // The backend enum is the authority; the form keeps the explicit closed set.
    model: form.attributionModel,
  };
  if (form.attributionModel === 'time_decay') {
    const halfLife = Number(form.attributionHalfLife);
    if (!Number.isInteger(halfLife) || halfLife < 1 || halfLife > 604800)
      throw new AnalysisValidationError(
        'attribution',
        '时间衰减模型须提供 1 至 604800 秒的半衰期整数。',
      );
    attribution.decay_half_life_seconds = halfLife;
  }
  return attribution;
}
function readHeatmap(form: AnalysisForm): NonNullable<QueryRequest['heatmap']> {
  const route = form.heatmapRoute;
  const maxLength = form.heatmapRouteField === 'route_name' ? 128 : 256;
  if (!route || [...route].length > maxLength || route.includes('\0'))
    throw new AnalysisValidationError(
      'heatmap',
      `${form.heatmapRouteField === 'route_name' ? '路由名称' : '路由模板'}须为 1 至 ${maxLength} 个字符。`,
    );
  const mode = form.heatmapMode === 'elements' ? { mode: 'elements' as const } : {};
  if (form.heatmapViewportScope === 'all') {
    return {
      [form.heatmapRouteField]: route,
      ...mode,
      viewport_scope: 'all',
    };
  }
  if (form.heatmapViewportScope === 'bucket') {
    if (
      form.heatmapViewportBucket !== 'mobile' &&
      form.heatmapViewportBucket !== 'tablet' &&
      form.heatmapViewportBucket !== 'desktop'
    )
      throw new AnalysisValidationError('heatmap', '视口分档须为移动端、平板或桌面端。');
    return {
      [form.heatmapRouteField]: route,
      ...mode,
      viewport_scope: 'bucket',
      viewport_bucket: form.heatmapViewportBucket,
    };
  }
  const width = Number(form.heatmapViewportWidth);
  const height = Number(form.heatmapViewportHeight);
  if (
    !Number.isInteger(width) ||
    width < 1 ||
    width > 65535 ||
    !Number.isInteger(height) ||
    height < 1 ||
    height > 65535
  )
    throw new AnalysisValidationError('heatmap', '视口宽高须为 1 至 65535 的整数。');
  return {
    [form.heatmapRouteField]: route,
    ...mode,
    viewport_width: width,
    viewport_height: height,
  };
}
export type AnalysisEditorSection =
  'range' | 'settings' | 'filters' | 'funnel' | 'retention' | 'segment' | 'path' | 'attribution';
export function validateAnalysisSection(form: AnalysisForm, section: AnalysisEditorSection): void {
  if (section === 'range') readTimeRange(form);
  else if (section === 'settings') readLimit(form);
  else if (section === 'filters') readFilters(form);
  else if (section === 'funnel') readFunnel(form);
  else if (section === 'retention') readRetention(form);
  else if (section === 'segment') readSegment(form);
  else if (section === 'path') readPath(form);
  else readAttribution(form);
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
  if (
    form.mode !== 'funnel' &&
    form.mode !== 'retention' &&
    form.mode !== 'segment' &&
    form.mode !== 'path' &&
    form.mode !== 'attribution' &&
    form.mode !== 'heatmap' &&
    form.event
  )
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
    const size = { minute: 60000, hour: 3600000, day: 86400000, week: 604800000 }[form.interval];
    if (form.mode === 'trend' && Math.ceil((to.getTime() - from.getTime()) / size) + 1 > limit)
      throw new AnalysisValidationError(
        'interval',
        '当前粒度可能超出结果行数，请增加行数、缩短时间或选择更大粒度。',
      );
  }
  const args = form.mode === 'heatmap' ? [] : readFilters(form);
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
    request.subject = form.subject;
    request.retention = readRetention(form);
  }
  if (form.mode === 'segment') {
    request.subject = form.subject;
    request.segment = readSegment(form);
  }
  if (form.mode === 'path') {
    request.subject = form.subject;
    request.path = readPath(form);
  }
  if (form.mode === 'attribution') {
    request.subject = form.subject;
    request.attribution = readAttribution(form);
  }
  if (form.mode === 'heatmap') request.heatmap = readHeatmap(form);
  return request;
}
export function metricLabel(request: QueryRequest, semanticMetric?: AnalysisForm['metric']) {
  const metric = request.metrics?.[0];
  if (metric?.op === 'unique')
    return `${request.subject === 'anonymous' ? '匿名标识' : '原始 UID'} UV`;
  return semanticMetric === 'pv' ? 'PV' : '事件数';
}

export const HEATMAP_COLUMNS = 20;
export const HEATMAP_ROWS = 10;
export type HeatmapCell = { x: number; y: number; clicks: number };
const heatmapPalette = ['#60a5fa', '#3b82f6', '#2563eb', '#1e40af', '#172554'] as const;

export function heatmapCells(rows: Record<string, unknown>[]): HeatmapCell[] {
  const clicks = new Map<string, number>();
  for (const row of rows) {
    const x = row.x_bin;
    const y = row.y_bin;
    const value = row.clicks;
    if (
      typeof x === 'number' &&
      Number.isInteger(x) &&
      x >= 0 &&
      x < HEATMAP_COLUMNS &&
      typeof y === 'number' &&
      Number.isInteger(y) &&
      y >= 0 &&
      y < HEATMAP_ROWS &&
      typeof value === 'number' &&
      Number.isSafeInteger(value) &&
      value >= 0
    )
      clicks.set(`${x}:${y}`, value);
  }
  return Array.from({ length: HEATMAP_COLUMNS * HEATMAP_ROWS }, (_, index) => {
    const x = index % HEATMAP_COLUMNS;
    const y = Math.floor(index / HEATMAP_COLUMNS);
    return { x, y, clicks: clicks.get(`${x}:${y}`) ?? 0 };
  });
}

export function heatmapCellColor(clicks: number, maximum: number): string {
  if (clicks <= 0 || maximum <= 0) return 'var(--color-surface-subtle)';
  const index = Math.min(
    heatmapPalette.length - 1,
    Math.max(0, Math.ceil((clicks / maximum) * heatmapPalette.length) - 1),
  );
  return heatmapPalette[index]!;
}

const heatmapBucketLabels: Record<'mobile' | 'tablet' | 'desktop', string> = {
  mobile: '移动端视口（< 768px）',
  tablet: '平板视口（768–1023px）',
  desktop: '桌面视口（≥ 1024px）',
};

export function heatmapViewportLabel(heatmap?: QueryRequest['heatmap']): string {
  if (!heatmap) return '未指定视口';
  if (heatmap.viewport_scope === 'all') return '全部视口聚合';
  if (heatmap.viewport_scope === 'bucket' && heatmap.viewport_bucket)
    return heatmapBucketLabels[heatmap.viewport_bucket];
  return `${(heatmap.viewport_width ?? 0).toLocaleString('zh-CN')} × ${(heatmap.viewport_height ?? 0).toLocaleString('zh-CN')} 视口`;
}

export function heatmapFormViewportLabel(form: AnalysisForm): string {
  if (form.heatmapViewportScope === 'all') return '全部视口聚合';
  if (form.heatmapViewportScope === 'bucket')
    return heatmapBucketLabels[form.heatmapViewportBucket];
  return `${form.heatmapViewportWidth} × ${form.heatmapViewportHeight} 视口`;
}

export function heatmapGridAspect(heatmap: NonNullable<QueryRequest['heatmap']>): {
  width: number;
  height: number;
  label: string;
} {
  if (heatmap.viewport_scope === 'all')
    return { width: 1440, height: 900, label: '全部视口聚合（归一化比例）' };
  if (heatmap.viewport_scope === 'bucket') {
    if (heatmap.viewport_bucket === 'mobile')
      return { width: 390, height: 700, label: heatmapBucketLabels.mobile };
    if (heatmap.viewport_bucket === 'tablet')
      return { width: 834, height: 1024, label: heatmapBucketLabels.tablet };
    return { width: 1440, height: 900, label: heatmapBucketLabels.desktop };
  }
  const width = heatmap.viewport_width ?? 1366;
  const height = heatmap.viewport_height ?? 900;
  return {
    width,
    height,
    label: `${width.toLocaleString('zh-CN')} × ${height.toLocaleString('zh-CN')} 像素`,
  };
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
