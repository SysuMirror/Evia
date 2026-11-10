import type { MetricChange, MetricDefinition } from '@evia/api-client';

export const metricSubjects = ['uid', 'anonymous'] as const;
export type MetricSubject = (typeof metricSubjects)[number];

export const metricOps = ['count', 'unique', 'sum', 'avg', 'min', 'max', 'p95'] as const;
export type MetricOp = (typeof metricOps)[number];

export type MetricUnit =
  | 'event'
  | 'raw_uid'
  | 'anonymous_id'
  | 'distinct_value'
  | 'number'
  | 'millisecond'
  | 'second'
  | 'byte';

export const numericOps: MetricOp[] = ['sum', 'avg', 'min', 'max', 'p95'];
export const approximateOps: MetricOp[] = ['unique', 'p95'];

export const opLabels: Record<MetricOp, string> = {
  count: '次数 count',
  unique: '去重 unique',
  sum: '求和 sum',
  avg: '平均 avg',
  min: '最小 min',
  max: '最大 max',
  p95: 'P95',
};

export const subjectLabels: Record<MetricSubject, string> = {
  uid: '原始 UID',
  anonymous: '匿名标识',
};

export const unitLabels: Record<MetricUnit, string> = {
  event: '事件次数 event',
  raw_uid: '原始 UID raw_uid',
  anonymous_id: '匿名标识 anonymous_id',
  distinct_value: '不同值 distinct_value',
  number: '数值 number',
  millisecond: '毫秒 millisecond',
  second: '秒 second',
  byte: '字节 byte',
};

export const allUnits = Object.keys(unitLabels) as MetricUnit[];

export interface EventVersionInput {
  event_key: string;
  schema_version: string;
}

export interface MetricFormState {
  title: string;
  description: string;
  subject: MetricSubject;
  op: MetricOp;
  accuracy: 'exact' | 'approximate';
  // Property key for numeric aggregations or distinct-value unique.
  fieldEnabled: boolean;
  fieldKey: string;
  unit: MetricUnit;
  events: EventVersionInput[];
  // Optional fixed filter tree as raw JSON; blank means no fixed filter.
  filterJSON: string;
}

export function isNumericOp(op: string): op is MetricOp {
  return (numericOps as readonly string[]).includes(op);
}

export function supportsApproximate(op: string): boolean {
  return (approximateOps as readonly string[]).includes(op);
}

/** Units the authoritative contract accepts for the chosen aggregation. */
export function compatibleUnits(state: {
  op: MetricOp;
  subject: MetricSubject;
  fieldEnabled: boolean;
}): MetricUnit[] {
  const { op, subject, fieldEnabled } = state;
  if (op === 'count') return ['event'];
  if (op === 'unique') {
    if (fieldEnabled) return ['distinct_value'];
    return subject === 'uid' ? ['raw_uid'] : ['anonymous_id'];
  }
  return ['number', 'millisecond', 'second', 'byte'];
}

export function defaultUnit(state: {
  op: MetricOp;
  subject: MetricSubject;
  fieldEnabled: boolean;
}): MetricUnit {
  return compatibleUnits(state)[0]!;
}

export function defaultFormState(): MetricFormState {
  return {
    title: '',
    description: '',
    subject: 'uid',
    op: 'count',
    accuracy: 'exact',
    fieldEnabled: false,
    fieldKey: '',
    unit: 'event',
    events: [{ event_key: '', schema_version: '1' }],
    filterJSON: '',
  };
}

function normalizeVersion(value: string): number | undefined {
  if (!/^\d+$/.test(value.trim())) return undefined;
  const version = Number(value.trim());
  return version >= 1 ? version : undefined;
}

/** Convert the form into a wire definition, returning client-visible errors. */
export function toDefinition(state: MetricFormState): {
  definition?: MetricDefinition;
  errors: string[];
} {
  const errors: string[] = [];
  const title = state.title.trim();
  if (title.length < 1 || title.length > 128) errors.push('指标标题需为 1–128 个非空白字符。');
  if (state.description.length > 2000) errors.push('描述不能超过 2000 个字符。');

  const events: MetricDefinition['event_versions'] = [];
  const seen = new Set<string>();
  for (const event of state.events) {
    const key = event.event_key.trim();
    const version = normalizeVersion(event.schema_version);
    if (!/^[a-z][a-z0-9_]{0,63}$/.test(key) || !version) {
      errors.push('每个事件需要合法事件名（小写字母开头）和不小于 1 的版本号。');
      continue;
    }
    const dedupe = `${key}@${version}`;
    if (seen.has(dedupe)) errors.push(`事件 ${key} 版本 ${version} 重复，请只保留一个版本。`);
    seen.add(dedupe);
    events.push({ event_key: key, schema_version: version });
  }
  if (events.length < 1) errors.push('至少固定一个已发布事件版本。');
  if (events.length > 20) errors.push('最多固定 20 个事件版本。');

  const needsField = state.fieldEnabled || isNumericOp(state.op);
  let field: MetricDefinition['aggregation']['field'];
  if (needsField) {
    const key = state.fieldKey.trim();
    if (!/^[a-z][a-z0-9_]{0,63}$/.test(key))
      errors.push('数值/去重字段名需为小写字母开头的 snake_case 标识。');
    const first = events[0];
    if (first)
      field = {
        kind: 'property',
        event_key: first.event_key,
        schema_version: first.schema_version,
        key,
      };
  }

  let filter: MetricDefinition['filter'];
  if (state.filterJSON.trim()) {
    try {
      filter = JSON.parse(state.filterJSON) as MetricDefinition['filter'];
      if (!filter || typeof filter !== 'object') errors.push('固定筛选必须是条件树对象。');
    } catch {
      errors.push('固定筛选不是合法 JSON。');
    }
  }

  const allowed = new Set(compatibleUnits(state));
  if (!allowed.has(state.unit)) errors.push(`单位 ${state.unit} 与聚合 ${state.op} 不兼容。`);

  if (errors.length || !events.length) return { errors };

  const aggregation: MetricDefinition['aggregation'] = { op: state.op };
  if (field) aggregation.field = field;
  if (supportsApproximate(state.op)) aggregation.accuracy = state.accuracy;

  const definition: MetricDefinition = {
    title,
    event_versions: events,
    subject: state.subject,
    aggregation,
    unit: state.unit,
  };
  if (state.description.trim()) definition.description = state.description.trim();
  if (filter) definition.filter = filter;
  return { definition, errors: [] };
}

/** Populate the editable form from a stored/normalized wire definition. */
export function fromDefinition(definition: MetricDefinition): MetricFormState {
  const state = defaultFormState();
  state.title = definition.title;
  state.description = definition.description ?? '';
  state.subject = definition.subject;
  state.op = definition.aggregation.op;
  state.accuracy = definition.aggregation.accuracy ?? 'exact';
  state.unit = definition.unit;
  state.events = definition.event_versions.map((event) => ({
    event_key: event.event_key,
    schema_version: String(event.schema_version),
  }));
  if (definition.aggregation.field) {
    state.fieldEnabled = true;
    state.fieldKey = definition.aggregation.field.key;
  }
  if (definition.filter) state.filterJSON = JSON.stringify(definition.filter, null, 2);
  return state;
}

export const changeCategoryLabels: Record<MetricChange['category'], string> = {
  definition: '定义',
  metadata: '元数据',
  event_versions: '事件版本',
  filter: '固定筛选',
  subject: '统计主体',
  aggregation: '聚合',
  unit: '单位',
};

export function describeChange(change: MetricChange): string {
  const category = changeCategoryLabels[change.category] ?? change.category;
  return `${category} · ${change.path}`;
}

export const MAX_METRIC_RANGE_DAYS = 31;
export const MAX_GROUP_BY = 3;
const SNAKE_KEY = /^[a-z][a-z0-9_]{0,63}$/;

export interface GroupByInput {
  event_key: string;
  schema_version: string;
  key: string;
}

export interface MetricRunForm {
  from: string;
  to: string;
  timezone: string;
  interval: '' | 'minute' | 'hour' | 'day';
  limit: string;
  filterJSON: string;
  groupBy: GroupByInput[];
}

export function defaultRunForm(timezone: string, now: Date = new Date()): MetricRunForm {
  const to = now.getTime();
  const from = to - 7 * 86400_000;
  return {
    from: new Date(from).toISOString().slice(0, 16),
    to: new Date(to).toISOString().slice(0, 16),
    timezone,
    interval: '',
    limit: '200',
    filterJSON: '',
    groupBy: [],
  };
}

export function buildRunExecute(form: MetricRunForm): {
  execute?: Record<string, unknown>;
  error?: string;
} {
  const from = new Date(form.from);
  const to = new Date(form.to);
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()))
    return { error: '请提供有效的运行起止时间。' };
  if (!(to.getTime() > from.getTime())) return { error: '结束时间必须晚于开始时间。' };
  if (to.getTime() - from.getTime() > MAX_METRIC_RANGE_DAYS * 86400_000)
    return { error: `运行范围最长 ${MAX_METRIC_RANGE_DAYS} 天。` };
  const limit = Number(form.limit);
  if (!Number.isInteger(limit) || limit < 1 || limit > 500)
    return { error: '结果行数上限需为 1–500 的整数。' };
  if (form.groupBy.length > MAX_GROUP_BY) return { error: `最多按 ${MAX_GROUP_BY} 个字段分组。` };

  const execute: Record<string, unknown> = {
    time_range: { from: from.toISOString(), to: to.toISOString() },
    timezone: form.timezone.trim() || 'UTC',
    limit,
  };
  if (form.interval) execute.interval = form.interval;
  if (form.filterJSON.trim()) {
    try {
      execute.filter = JSON.parse(form.filterJSON);
    } catch {
      return { error: '运行筛选不是合法 JSON。' };
    }
  }
  const groupBy = form.groupBy
    .filter((entry) => entry.key.trim() || entry.event_key.trim())
    .map((entry) => {
      const version = Number(entry.schema_version);
      return {
        kind: 'property' as const,
        event_key: entry.event_key.trim(),
        schema_version: version,
        key: entry.key.trim(),
      };
    });
  for (const entry of groupBy) {
    if (!SNAKE_KEY.test(entry.event_key) || entry.schema_version < 1)
      return { error: '分组字段引用的事件名或版本无效。' };
    if (!SNAKE_KEY.test(entry.key))
      return { error: '分组属性名需为小写字母开头的 snake_case 标识。' };
  }
  if (groupBy.length) execute.group_by = groupBy;
  return { execute };
}

export function formatMetricInstant(instant: string): string {
  const parsed = new Date(instant);
  if (Number.isNaN(parsed.getTime())) return instant;
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(
    parsed.getHours(),
  )}:${pad(parsed.getMinutes())}`;
}
