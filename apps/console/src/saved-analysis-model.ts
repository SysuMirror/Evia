import type {
  MetricReference,
  SavedAnalysisDefinition,
  SavedAnalysisDefinitionInput,
  SavedAnalysisTemplate,
} from '@evia/api-client';
export type {
  MetricReference,
  SavedAnalysisDefinition,
  SavedAnalysisDefinitionInput,
  SavedAnalysisTemplate,
} from '@evia/api-client';
import {
  fieldOperators,
  operators,
  systemFields,
  type QueryField,
  type QueryFilter,
} from './analysis-model';

export type SavedMetric = NonNullable<SavedAnalysisTemplate['metrics']>[number];
export type SavedPin = NonNullable<SavedAnalysisTemplate['event_versions']>[number];
export type SavedVisualization = SavedAnalysisDefinition['visualization']['kind'];
export type SavedIssue = { path: string; message: string };
export type SavedLeafFilter = Exclude<QueryFilter, { args: unknown }>;
export const savedVisualizationLabels: Record<SavedVisualization, string> = {
  metric: '指标',
  line: '折线图',
  bar: '柱状图',
  table: '表格',
  funnel: '漏斗图',
};
export const savedMetricLabels: Record<SavedMetric['op'], string> = {
  count: '事件数',
  unique: '去重数',
  sum: '合计',
  avg: '平均值',
  min: '最小值',
  max: '最大值',
  p95: 'P95',
};
export function initialSavedAnalysisDefinition(): SavedAnalysisDefinition {
  return {
    title: '',
    description: '',
    query: {
      kind: 'trend',
      event_versions: [],
      metrics: [{ op: 'count', accuracy: 'exact' }],
      limit: 200,
    },
    visualization: { kind: 'metric' },
  };
}
export type SavedAnalysisSource = 'inline' | 'metric';
/** A metric-backed analysis is recognized solely by the presence of its pin. */
export function isMetricReferenceDefinition(
  value: SavedAnalysisDefinition,
): value is SavedAnalysisDefinition & { metric_ref: MetricReference } {
  return value.metric_ref !== undefined;
}
/** Unselected sentinel: type-valid but rejected by local issues until a metric and version are picked. */
export function emptyMetricReference(): MetricReference {
  return { metric_id: '', version: 0 };
}
function placeholderMetricQuery(): SavedAnalysisTemplate {
  return {
    kind: 'trend',
    event_versions: [],
    metrics: [{ op: 'count', accuracy: 'exact' }],
    limit: 200,
  };
}
/** Switch the data source. Metric mode keeps a placeholder query only to satisfy the stored read type locally. */
export function switchSavedAnalysisSource(
  value: SavedAnalysisDefinition,
  source: SavedAnalysisSource,
): SavedAnalysisDefinition {
  if (source === 'metric') {
    if (isMetricReferenceDefinition(value)) return cloneSavedDefinition(value);
    return {
      ...cloneSavedDefinition(value),
      query: placeholderMetricQuery(),
      metric_ref: emptyMetricReference(),
      visualization: { kind: 'metric' },
    };
  }
  if (!isMetricReferenceDefinition(value)) return cloneSavedDefinition(value);
  const next = cloneSavedDefinition(value);
  delete next.metric_ref;
  return next;
}
export function patchMetricReference(
  value: SavedAnalysisDefinition,
  ref: MetricReference,
): SavedAnalysisDefinition {
  return {
    ...cloneSavedDefinition(value),
    metric_ref: structuredClone(ref),
    visualization: { kind: 'metric' },
  };
}
/**
 * Build the write model. A metric reference omits the inline query because the
 * server derives it from the pinned immutable metric; inline analyses send the
 * full definition unchanged.
 */
export function toSavedAnalysisInput(value: SavedAnalysisDefinition): SavedAnalysisDefinitionInput {
  if (isMetricReferenceDefinition(value)) {
    return {
      title: value.title,
      description: value.description,
      metric_ref: structuredClone(value.metric_ref),
      visualization: { kind: 'metric' },
    };
  }
  return cloneSavedDefinition(value);
}
export function validMetricReference(ref: MetricReference | undefined): boolean {
  return (
    !!ref &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(ref.metric_id) &&
    Number.isSafeInteger(ref.version) &&
    ref.version >= 1
  );
}
export function cloneSavedDefinition(value: SavedAnalysisDefinition): SavedAnalysisDefinition {
  return structuredClone(value);
}
/** Change only the supplied fields; untouched nested/advanced configuration stays intact. */
export function patchSavedQuery(
  value: SavedAnalysisDefinition,
  patch: Partial<SavedAnalysisTemplate>,
): SavedAnalysisDefinition {
  return { ...value, query: { ...value.query, ...structuredClone(patch) } };
}
/** Called only after explicit confirmation that the old query is being replaced. */
export function replaceSavedKind(
  value: SavedAnalysisDefinition,
  kind: SavedAnalysisTemplate['kind'],
): SavedAnalysisDefinition {
  if (kind === value.query.kind) return cloneSavedDefinition(value);
  const limit = value.query.limit ?? 200;
  let query: SavedAnalysisTemplate;
  let visualization: SavedVisualization;
  if (kind === 'trend') {
    query = {
      kind,
      event_versions: [],
      metrics: [{ op: 'count', accuracy: 'exact' }],
      limit,
    };
    visualization = 'metric';
  } else if (kind === 'retention') {
    query = {
      kind,
      subject: 'uid',
      limit,
      retention: {
        start_event: { event_key: '', schema_version: 0 },
        return_event: { event_key: '', schema_version: 0 },
        granularity: 'day',
        periods: 4,
      },
    };
    visualization = 'table';
  } else if (kind === 'segment') {
    query = {
      kind,
      subject: 'uid',
      limit,
      segment: {
        event: { event_key: '', schema_version: 0 },
        field: { kind: 'system', key: 'event_key' },
      },
    };
    visualization = 'table';
  } else if (kind === 'path') {
    query = {
      kind,
      subject: 'uid',
      limit,
      path: { depth: 3 },
    };
    visualization = 'table';
  } else if (kind === 'attribution') {
    query = {
      kind,
      subject: 'uid',
      limit,
      attribution: {
        conversion: { event_key: '', schema_version: 0 },
        touchpoints: [{ event_key: '', schema_version: 0 }],
        window_seconds: 86400,
        model: 'last_touch',
      },
    };
    visualization = 'table';
  } else {
    query = {
      kind,
      subject: 'uid',
      limit,
      funnel: {
        steps: [
          { event_key: '', schema_version: 1 },
          { event_key: '', schema_version: 1 },
        ],
        window_seconds: 3600,
        mode: 'ordered_with_interleaving',
      },
    };
    visualization = 'funnel';
  }
  return { ...value, query, visualization: { kind: visualization } };
}
export function savedPins(query: SavedAnalysisTemplate): SavedPin[] {
  let pins: SavedPin[] = query.event_versions ?? [];
  if (query.kind === 'funnel') pins = query.funnel?.steps ?? [];
  else if (query.kind === 'retention' && query.retention)
    pins = [query.retention.start_event, query.retention.return_event];
  else if (query.kind === 'segment' && query.segment) pins = [query.segment.event];
  else if (query.kind === 'path' && query.path?.start_event) pins = [query.path.start_event];
  else if (query.kind === 'attribution' && query.attribution)
    pins = [query.attribution.conversion, ...query.attribution.touchpoints];
  return pins
    .filter(
      (pin, index) =>
        pins.findIndex(
          (other) =>
            other.event_key === pin.event_key && other.schema_version === pin.schema_version,
        ) === index,
    )
    .map(({ event_key, schema_version }) => ({ event_key, schema_version }));
}
export function savedFieldID(field: QueryField): string {
  return field.kind === 'system'
    ? `system:${field.key}`
    : `${field.event_key}:${field.schema_version}:${field.key}`;
}
export function savedFieldLabel(field: QueryField): string {
  return field.kind === 'system'
    ? (systemFields.find((item) => item.key === field.key)?.label ?? field.key)
    : `${field.event_key} v${field.schema_version} / ${field.key}`;
}
export function flatSavedFilters(
  filter: QueryFilter | undefined,
): { join: 'and' | 'or'; rows: SavedLeafFilter[] } | null {
  if (!filter) return { join: 'and', rows: [] };
  if (!('args' in filter)) return { join: 'and', rows: [structuredClone(filter)] };
  if (filter.args.some((item) => 'args' in item)) return null;
  return { join: filter.op, rows: structuredClone(filter.args) as SavedLeafFilter[] };
}
export function savedFilterText(filter: SavedLeafFilter): string {
  if (!('value' in filter)) return '';
  return typeof filter.value === 'string' ? filter.value : JSON.stringify(filter.value);
}
export function parseSavedFilterValue(
  text: string,
  type: string,
  op: string,
): string | number | boolean | (string | number | boolean)[] {
  if (!fieldOperators('', type).includes(op)) throw new Error('筛选运算与字段类型不兼容。');
  const array = op === 'in' || (type.startsWith('array:') && op !== 'contains');
  const kind = type.replace('array:', '');
  let input: unknown = text;
  if (array) {
    try {
      input = parseSavedJSON(text);
    } catch {
      throw new Error('请输入有效的 JSON 数组，保留值的真实类型。');
    }
    if (!Array.isArray(input) || input.length > 20)
      throw new Error('筛选值须为最多 20 项的 JSON 数组。');
  }
  const values = (array ? (input as unknown[]) : [input]).map((item) => {
    if (kind === 'boolean') {
      if (array && typeof item === 'boolean') return item;
      if (!array && (item === 'true' || item === 'false')) return item === 'true';
      throw new Error('布尔值请输入 true 或 false。');
    }
    if (kind === 'number' || kind === 'integer') {
      if (array ? typeof item !== 'number' : typeof item !== 'string' || !item.trim())
        throw new Error('请输入真实数值。');
      const number = Number(item);
      if (
        !Number.isFinite(number) ||
        Math.abs(number) > Number.MAX_SAFE_INTEGER ||
        (kind === 'integer' && !Number.isSafeInteger(number))
      )
        throw new Error('数值超出范围或不是有效整数。');
      return number;
    }
    if (typeof item !== 'string' || [...item].length > 1024)
      throw new Error('字符串最多 1024 字。');
    if (
      kind === 'datetime' &&
      (!/^\d{4}-\d\d-\d\dT.*(?:Z|[+-]\d\d:\d\d)$/.test(item) || !Number.isFinite(Date.parse(item)))
    )
      throw new Error('时间值须为包含时区的有效时间。');
    return item;
  });
  return array ? values : values[0]!;
}
export function savedVisualizationCompatible(
  query: SavedAnalysisTemplate,
  kind: SavedVisualization,
): boolean {
  if (kind === 'table') return true;
  if (kind === 'funnel') return query.kind === 'funnel';
  if (query.kind !== 'trend') return false;
  if (kind === 'metric')
    return (query.metrics?.length || 1) === 1 && !query.group_by?.length && !query.interval;
  if (kind === 'line') return !!query.interval;
  return !!query.interval || !!query.group_by?.length;
}
export function savedDefinitionSummary(value: SavedAnalysisDefinition): string {
  if (isMetricReferenceDefinition(value)) {
    const ref = value.metric_ref;
    return validMetricReference(ref)
      ? `指标引用：${ref.metric_id} v${ref.version}（整段指标卡片）`
      : '指标引用：请选择已发布指标与版本';
  }
  const q = value.query;
  if (q.kind === 'funnel')
    return `${q.funnel?.steps.length ?? 0} 个有序步骤，${q.funnel?.window_seconds ?? 0} 秒窗口`;
  if (q.kind === 'retention' && q.retention)
    return `留存 ${q.retention.periods} 期（${q.retention.granularity === 'week' ? '按周' : q.retention.granularity === 'month' ? '按月' : '按天'}），起止事件各 1 个固定版本`;
  if (q.kind === 'segment' && q.segment) return '分群：按主体最早分群事件的标量字段唯一归组';
  if (q.kind === 'path' && q.path)
    return `路径深度 ${q.path.depth}${q.path.start_event ? '，钉选起始事件' : '，读取窗口内全部事件'}`;
  if (q.kind === 'attribution' && q.attribution)
    return `归因：${q.attribution.touchpoints.length} 个触点，窗口 ${q.attribution.window_seconds} 秒`;
  return `${q.event_versions?.length ?? 0} 个固定版本，${q.metrics?.length || 1} 个指标，${q.group_by?.length ?? 0} 个分组`;
}
/** JSON.parse alone would silently replace duplicate keys and overflow numbers. */
export function parseSavedJSON(text: string): unknown {
  if (new TextEncoder().encode(text).length > 128 * 1024) throw new Error('分析定义最多 128 KiB。');
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new Error('请输入有效的 JSON。');
  }
  const stack: { object: boolean; keys: Set<string>; key: boolean }[] = [];
  for (const match of text.matchAll(/"(?:\\.|[^"\\])*"|[{}\[\]:,]/g)) {
    const token = match[0],
      top = stack.at(-1);
    if (token === '{' || token === '[')
      stack.push({ object: token === '{', keys: new Set(), key: token === '{' });
    else if (token === '}' || token === ']') stack.pop();
    else if (token === ',' && top?.object) top.key = true;
    else if (token === ':' && top?.object) top.key = false;
    else if (token.startsWith('"') && top?.object && top.key) {
      const key = JSON.parse(token) as string;
      if (top.keys.has(key)) throw new Error(`JSON 字段重复：${key}`);
      top.keys.add(key);
      top.key = false;
    }
  }
  function numbers(node: unknown, depth = 0): void {
    if (depth > 24) throw new Error('JSON 嵌套过深。');
    if (
      typeof node === 'number' &&
      (!Number.isFinite(node) || Math.abs(node) > Number.MAX_SAFE_INTEGER)
    )
      throw new Error('JSON 包含超出安全范围的数值。');
    if (Array.isArray(node)) node.forEach((child) => numbers(child, depth + 1));
    else if (node && typeof node === 'object')
      Object.values(node).forEach((child) => numbers(child, depth + 1));
  }
  numbers(value);
  return value;
}

/** Local form feedback; authorization, published membership and types remain server-validated. */
export function savedAnalysisIssues(value: SavedAnalysisDefinition): SavedIssue[] {
  const issues: SavedIssue[] = [];
  const add = (path: string, message: string) => {
    if (issues.length < 32) issues.push({ path, message });
  };
  const object = (
    node: unknown,
    allowed: string[],
    path: string,
  ): node is Record<string, unknown> => {
    if (!node || typeof node !== 'object' || Array.isArray(node)) {
      add(path, '须为对象。');
      return false;
    }
    for (const key of Object.keys(node))
      if (!allowed.includes(key)) add(`${path}/${key}`, '不支持此字段。');
    return true;
  };
  const safe = (node: unknown, min: number, max: number, path: string) => {
    if (typeof node !== 'number' || !Number.isSafeInteger(node) || node < min || node > max)
      add(path, `须为 ${min} 至 ${max} 的安全整数。`);
  };
  const eventKey = (node: unknown, path: string) => {
    if (typeof node !== 'string' || !/^(sys\.)?[a-z][a-z0-9_]{0,63}$/.test(node))
      add(path, '请选择有效的事件。');
  };
  const pin = (node: unknown, path: string) => {
    if (!object(node, ['event_key', 'schema_version'], path)) return;
    eventKey(node.event_key, `${path}/event_key`);
    safe(node.schema_version, 1, Number.MAX_SAFE_INTEGER, `${path}/schema_version`);
  };
  if (!object(value, ['title', 'description', 'query', 'metric_ref', 'visualization'], ''))
    return issues;
  if (
    typeof value.title !== 'string' ||
    !value.title.trim() ||
    [...value.title.trim()].length > 128 ||
    value.title.includes('\0')
  )
    add('/title', '标题须为 1 至 128 字。');
  if (
    value.description !== undefined &&
    (typeof value.description !== 'string' ||
      [...value.description].length > 2000 ||
      value.description.includes('\0'))
  )
    add('/description', '描述最多 2000 字。');
  // A metric-referencing analysis validates only its pin and the metric card;
  // the derived inline query is server-owned and never edited through this form.
  if (value.metric_ref !== undefined) {
    if (value.visualization.kind !== 'metric')
      add('/visualization/kind', '指标引用仅支持指标卡片。');
    const ref = value.metric_ref;
    if (!object(ref, ['metric_id', 'version'], '/metric_ref')) return issues;
    if (
      typeof ref.metric_id !== 'string' ||
      !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(ref.metric_id)
    )
      add('/metric_ref/metric_id', '请选择已发布指标。');
    if (typeof ref.version !== 'number' || !Number.isSafeInteger(ref.version) || ref.version < 1)
      add('/metric_ref/version', '请选择指标版本。');
    return issues;
  }
  const q = value.query;
  if (
    !object(
      q,
      [
        'kind',
        'event_versions',
        'filter',
        'subject',
        'group_by',
        'metrics',
        'interval',
        'funnel',
        'retention',
        'segment',
        'path',
        'attribution',
        'limit',
      ],
      '/query',
    )
  )
    return issues;
  if (!['trend', 'funnel', 'retention', 'segment', 'path', 'attribution'].includes(q.kind))
    add('/query/kind', '仅支持趋势、漏斗、留存、分群、路径或归因。');
  if (q.subject !== undefined && q.subject !== 'uid' && q.subject !== 'anonymous')
    add('/query/subject', '请选择原始 UID 或匿名标识。');
  if (q.limit !== undefined) safe(q.limit, 1, 500, '/query/limit');
  if (q.interval !== undefined && !['minute', 'hour', 'day'].includes(q.interval))
    add('/query/interval', '时间粒度无效。');
  const pins: SavedPin[] = [];
  if (q.kind === 'trend') {
    if (
      !Array.isArray(q.event_versions) ||
      !q.event_versions.length ||
      q.event_versions.length > 20
    )
      add('/query/event_versions', '请选择 1 至 20 个明确的事件版本。');
    else
      q.event_versions.forEach((item, index) => {
        pin(item, `/query/event_versions/${index}`);
        if (item && typeof item.event_key === 'string') {
          if (pins.some((other) => other.event_key === item.event_key))
            add(`/query/event_versions/${index}`, '同一事件只能固定一个版本。');
          pins.push(item);
        }
      });
    if (q.funnel !== undefined) add('/query/funnel', '趋势配置不接受漏斗步骤。');
  } else if (q.kind === 'funnel') {
    if (q.event_versions !== undefined) add('/query/event_versions', '漏斗通过步骤固定版本。');
    if (q.interval !== undefined || q.metrics?.length || q.group_by?.length)
      add('/query', '漏斗不接受趋势指标、分组或时间粒度。');
    if (!q.subject) add('/query/subject', '请选择漏斗主体。');
    if (object(q.funnel, ['steps', 'window_seconds', 'mode', 'same_entity'], '/query/funnel')) {
      const f = q.funnel;
      if (!Array.isArray(f.steps) || f.steps.length < 2 || f.steps.length > 8)
        add('/query/funnel/steps', '漏斗须有 2 至 8 个有序步骤。');
      else
        f.steps.forEach((step, index) => {
          if (
            !object(step, ['event_key', 'schema_version', 'filter'], `/query/funnel/steps/${index}`)
          )
            return;
          eventKey(step.event_key, `/query/funnel/steps/${index}/event_key`);
          safe(
            step.schema_version,
            1,
            Number.MAX_SAFE_INTEGER,
            `/query/funnel/steps/${index}/schema_version`,
          );
          pins.push(step as SavedPin);
        });
      safe(f.window_seconds, 1, 604800, '/query/funnel/window_seconds');
      if (f.mode !== 'ordered_with_interleaving') add('/query/funnel/mode', '漏斗模式无效。');
    }
  } else if (q.kind === 'retention') {
    if (q.event_versions !== undefined) add('/query/event_versions', '留存通过起止事件固定版本。');
    if (
      q.interval !== undefined ||
      q.metrics?.length ||
      q.group_by?.length ||
      q.funnel !== undefined
    )
      add('/query', '留存不接受趋势指标、分组、时间粒度或漏斗。');
    if (!q.subject) add('/query/subject', '请选择留存主体。');
    const rt = q.retention;
    if (
      rt &&
      object(rt, ['start_event', 'return_event', 'granularity', 'periods'], '/query/retention')
    ) {
      if (rt.granularity !== 'day' && rt.granularity !== 'week' && rt.granularity !== 'month')
        add('/query/retention/granularity', '留存粒度必须是 day、week 或 month。');
      safe(rt.periods, 1, 12, '/query/retention/periods');
      if (
        object(
          rt.start_event,
          ['event_key', 'schema_version', 'filter'],
          '/query/retention/start_event',
        )
      ) {
        eventKey(rt.start_event.event_key, '/query/retention/start_event/event_key');
        safe(
          rt.start_event.schema_version,
          1,
          Number.MAX_SAFE_INTEGER,
          '/query/retention/start_event/schema_version',
        );
        pins.push(rt.start_event as SavedPin);
      }
      if (
        object(
          rt.return_event,
          ['event_key', 'schema_version', 'filter'],
          '/query/retention/return_event',
        )
      ) {
        eventKey(rt.return_event.event_key, '/query/retention/return_event/event_key');
        safe(
          rt.return_event.schema_version,
          1,
          Number.MAX_SAFE_INTEGER,
          '/query/retention/return_event/schema_version',
        );
        pins.push(rt.return_event as SavedPin);
      }
    }
  } else if (q.kind === 'segment') {
    if (q.event_versions !== undefined) add('/query/event_versions', '分群通过分群事件固定版本。');
    if (q.interval !== undefined || q.metrics?.length || q.group_by?.length)
      add('/query', '分群不接受趋势指标、分组或时间粒度。');
    if (
      q.funnel !== undefined ||
      q.retention !== undefined ||
      q.path !== undefined ||
      q.attribution !== undefined
    )
      add('/query', '分群仅接受 segment 配置块。');
    if (!q.subject) add('/query/subject', '请选择分群主体。');
    const sg = q.segment;
    if (sg && object(sg, ['event', 'field'], '/query/segment')) {
      if (object(sg.event, ['event_key', 'schema_version', 'filter'], '/query/segment/event')) {
        eventKey(sg.event.event_key, '/query/segment/event/event_key');
        safe(
          sg.event.schema_version,
          1,
          Number.MAX_SAFE_INTEGER,
          '/query/segment/event/schema_version',
        );
        pins.push(sg.event as SavedPin);
      }
      const sf = sg.field;
      if (object(sf, ['kind', 'key', 'event_key', 'schema_version'], '/query/segment/field')) {
        if (sf.kind === 'system') {
          if (
            ![
              'event_key',
              'uid',
              'anonymous_id',
              'session_id',
              'page_view_id',
              'operation_id',
            ].includes(String(sf.key))
          )
            add('/query/segment/field/key', '系统字段无效。');
        } else if (sf.kind === 'property') {
          eventKey(sf.event_key, '/query/segment/field/event_key');
          safe(
            sf.schema_version,
            1,
            Number.MAX_SAFE_INTEGER,
            '/query/segment/field/schema_version',
          );
          if (typeof sf.key !== 'string' || !/^[a-z][a-z0-9_]{0,63}$/.test(sf.key))
            add('/query/segment/field/key', '属性名无效。');
        } else {
          add('/query/segment/field/kind', '字段类型无效。');
        }
      }
    }
  } else if (q.kind === 'path') {
    if (q.event_versions !== undefined)
      add('/query/event_versions', '路径通过可选起始事件固定版本。');
    if (q.interval !== undefined || q.metrics?.length || q.group_by?.length)
      add('/query', '路径不接受趋势指标、分组或时间粒度。');
    if (
      q.funnel !== undefined ||
      q.retention !== undefined ||
      q.segment !== undefined ||
      q.attribution !== undefined
    )
      add('/query', '路径仅接受 path 配置块。');
    if (!q.subject) add('/query/subject', '请选择路径主体。');
    const pa = q.path;
    if (pa && object(pa, ['start_event', 'depth', 'collapse_repeats'], '/query/path')) {
      safe(pa.depth, 2, 8, '/query/path/depth');
      if (pa.collapse_repeats !== undefined && typeof pa.collapse_repeats !== 'boolean')
        add('/query/path/collapse_repeats', '折叠开关必须为布尔值。');
      if (
        pa.start_event &&
        object(pa.start_event, ['event_key', 'schema_version', 'filter'], '/query/path/start_event')
      ) {
        eventKey(pa.start_event.event_key, '/query/path/start_event/event_key');
        safe(
          pa.start_event.schema_version,
          1,
          Number.MAX_SAFE_INTEGER,
          '/query/path/start_event/schema_version',
        );
        pins.push(pa.start_event as SavedPin);
      }
    }
  } else if (q.kind === 'attribution') {
    if (q.event_versions !== undefined)
      add('/query/event_versions', '归因通过转化与触点固定版本。');
    if (q.interval !== undefined || q.metrics?.length || q.group_by?.length)
      add('/query', '归因不接受趋势指标、分组或时间粒度。');
    if (
      q.funnel !== undefined ||
      q.retention !== undefined ||
      q.segment !== undefined ||
      q.path !== undefined
    )
      add('/query', '归因仅接受 attribution 配置块。');
    if (!q.subject) add('/query/subject', '请选择归因主体。');
    const at = q.attribution;
    if (
      at &&
      object(
        at,
        ['conversion', 'touchpoints', 'window_seconds', 'model', 'decay_half_life_seconds'],
        '/query/attribution',
      )
    ) {
      if (
        !['last_touch', 'first_touch', 'linear', 'position_based', 'time_decay'].includes(at.model)
      )
        add('/query/attribution/model', '归因模型无效。');
      safe(at.window_seconds, 1, 604800, '/query/attribution/window_seconds');
      if (at.model === 'time_decay')
        safe(
          at.decay_half_life_seconds ?? 0,
          1,
          604800,
          '/query/attribution/decay_half_life_seconds',
        );
      if (
        object(
          at.conversion,
          ['event_key', 'schema_version', 'filter'],
          '/query/attribution/conversion',
        )
      ) {
        eventKey(at.conversion.event_key, '/query/attribution/conversion/event_key');
        safe(
          at.conversion.schema_version,
          1,
          Number.MAX_SAFE_INTEGER,
          '/query/attribution/conversion/schema_version',
        );
        pins.push(at.conversion as SavedPin);
      }
      if (
        !Array.isArray(at.touchpoints) ||
        at.touchpoints.length < 1 ||
        at.touchpoints.length > 8
      ) {
        add('/query/attribution/touchpoints', '归因触点须为 1 至 8 个。');
      } else {
        at.touchpoints.forEach((tp, index) => {
          if (
            !object(
              tp,
              ['event_key', 'schema_version', 'filter'],
              `/query/attribution/touchpoints/${index}`,
            )
          )
            return;
          eventKey(tp.event_key, `/query/attribution/touchpoints/${index}/event_key`);
          safe(
            tp.schema_version,
            1,
            Number.MAX_SAFE_INTEGER,
            `/query/attribution/touchpoints/${index}/schema_version`,
          );
          pins.push(tp as SavedPin);
        });
      }
    }
  }
  const field = (node: unknown, path: string) => {
    if (!object(node, ['kind', 'key', 'event_key', 'schema_version'], path)) return;
    if (node.kind === 'system') {
      if (
        !systemFields.some((item) => item.key === node.key) ||
        node.event_key !== undefined ||
        node.schema_version !== undefined
      )
        add(path, '系统字段无效。');
    } else if (node.kind === 'property') {
      eventKey(node.event_key, `${path}/event_key`);
      safe(node.schema_version, 1, Number.MAX_SAFE_INTEGER, `${path}/schema_version`);
      if (typeof node.key !== 'string' || !/^[a-z][a-z0-9_]{0,63}$/.test(node.key))
        add(`${path}/key`, '属性名无效。');
      if (
        !pins.some(
          (p) => p.event_key === node.event_key && p.schema_version === node.schema_version,
        )
      )
        add(path, '属性引用不属于当前固定版本，请同时更新引用与版本。');
    } else add(path, '字段类型无效。');
  };
  let filterNodes = 0;
  const filter = (node: unknown, path: string, depth = 0): void => {
    if (++filterNodes > 100 || depth > 8) {
      add(path, '筛选条件过多或嵌套过深。');
      return;
    }
    if (!node || typeof node !== 'object' || Array.isArray(node)) {
      add(path, '筛选须为对象。');
      return;
    }
    const f = node as Record<string, unknown>;
    if (f.op === 'and' || f.op === 'or') {
      object(f, ['op', 'args'], path);
      if (!Array.isArray(f.args) || !f.args.length || f.args.length > 20)
        add(path, '筛选组须有 1 至 20 个条件。');
      else f.args.forEach((child, index) => filter(child, `${path}/args/${index}`, depth + 1));
    } else {
      const unary = f.op === 'is_missing' || f.op === 'is_present';
      object(f, unary ? ['op', 'field'] : ['op', 'field', 'value'], path);
      if (typeof f.op !== 'string' || !Object.hasOwn(operators, f.op))
        add(`${path}/op`, '筛选运算无效。');
      field(f.field, `${path}/field`);
      if (!unary) {
        const scalar = (item: unknown) =>
          (typeof item === 'string' && [...item].length <= 1024) ||
          typeof item === 'boolean' ||
          (typeof item === 'number' &&
            Number.isFinite(item) &&
            Math.abs(item) <= Number.MAX_SAFE_INTEGER);
        if (
          Array.isArray(f.value) ? f.value.length > 20 || !f.value.every(scalar) : !scalar(f.value)
        )
          add(`${path}/value`, '筛选值类型或大小无效。');
      }
    }
  };
  if (q.filter !== undefined) filter(q.filter, '/query/filter');
  if (q.group_by !== undefined) {
    if (!Array.isArray(q.group_by) || q.group_by.length > 3)
      add('/query/group_by', '分组最多 3 项。');
    else q.group_by.forEach((item, index) => field(item, `/query/group_by/${index}`));
  }
  if (q.metrics !== undefined) {
    if (!Array.isArray(q.metrics) || q.metrics.length > 10)
      add('/query/metrics', '指标最多 10 项。');
    else
      q.metrics.forEach((metric, index) => {
        const path = `/query/metrics/${index}`;
        if (!object(metric, ['op', 'field', 'accuracy'], path)) return;
        if (!Object.hasOwn(savedMetricLabels, metric.op)) add(path, '指标运算无效。');
        if (metric.field !== undefined) field(metric.field, `${path}/field`);
        if (metric.accuracy !== undefined && !['exact', 'approximate'].includes(metric.accuracy))
          add(path, '精度无效。');
        if (metric.accuracy === 'approximate' && metric.op !== 'unique' && metric.op !== 'p95')
          add(path, '此指标只支持精确结果。');
        if (metric.op === 'count' && metric.field !== undefined) add(path, '事件数不接受字段。');
        if (metric.op !== 'count' && metric.op !== 'unique' && !metric.field)
          add(path, '此指标需要选择字段。');
        if (metric.op === 'unique' && !metric.field && !q.subject)
          add('/query/subject', '身份去重需要选择主体。');
      });
  }
  if (q.kind === 'funnel' && q.funnel && Array.isArray(q.funnel.steps)) {
    q.funnel.steps.forEach((step, index) => {
      if (step?.filter !== undefined) filter(step.filter, `/query/funnel/steps/${index}/filter`);
    });
    if (q.funnel.same_entity !== undefined) {
      if (!Array.isArray(q.funnel.same_entity) || q.funnel.same_entity.length > 8)
        add('/query/funnel/same_entity', '实体条件最多 8 项。');
      else
        q.funnel.same_entity.forEach((entity, index) => {
          const path = `/query/funnel/same_entity/${index}`;
          if (!object(entity, ['step_index', 'field'], path)) return;
          safe(entity.step_index, 0, q.funnel!.steps.length - 1, `${path}/step_index`);
          field(entity.field, `${path}/field`);
          const step = q.funnel!.steps[entity.step_index];
          if (
            step &&
            entity.field?.kind === 'property' &&
            (entity.field.event_key !== step.event_key ||
              entity.field.schema_version !== step.schema_version)
          )
            add(path, '实体字段须属于对应步骤的固定版本。');
        });
    }
  }
  if (object(value.visualization, ['kind'], '/visualization')) {
    if (!Object.hasOwn(savedVisualizationLabels, value.visualization.kind))
      add('/visualization/kind', '图表类型无效。');
    else if (!savedVisualizationCompatible(q, value.visualization.kind))
      add('/visualization/kind', '图表与当前指标、分组或时间粒度不兼容，请选择兼容图表。');
  }
  return issues;
}
export function parseSavedAnalysisDefinition(text: string): SavedAnalysisDefinition {
  const value = parseSavedJSON(text) as SavedAnalysisDefinition;
  const issues = savedAnalysisIssues(value);
  if (issues.length) throw new Error(`${issues[0]!.path || '/'}，${issues[0]!.message}`);
  return value;
}
