import { expect, test } from '@playwright/test';
import type {
  MetricReference,
  SavedAnalysisDefinition,
} from '../../apps/console/src/saved-analysis-model';
import {
  maxSavedAnalysisSearchRunes,
  normalizeSavedAnalysisSearch,
} from '../../apps/console/src/saved-analyses/model';
import {
  cloneSavedDefinition,
  emptyMetricReference,
  flatSavedFilters,
  initialSavedAnalysisDefinition,
  isMetricReferenceDefinition,
  parseSavedAnalysisDefinition,
  parseSavedFilterValue,
  parseSavedJSON,
  patchMetricReference,
  patchSavedQuery,
  replaceSavedKind,
  savedAnalysisIssues,
  savedDefinitionSummary,
  savedFilterText,
  savedPins,
  savedVisualizationCompatible,
  switchSavedAnalysisSource,
  toSavedAnalysisInput,
  validMetricReference,
} from '../../apps/console/src/saved-analysis-model';

const score = {
  kind: 'property' as const,
  event_key: 'activity_view',
  schema_version: 2,
  key: 'score',
};
const flag = {
  kind: 'property' as const,
  event_key: 'activity_view',
  schema_version: 2,
  key: 'enabled',
};
function definition(): SavedAnalysisDefinition {
  return {
    title: '活动分析',
    description: '保留完整配置',
    visualization: { kind: 'table' },
    query: {
      kind: 'trend',
      event_versions: [{ event_key: 'activity_view', schema_version: 2 }],
      metrics: [
        { op: 'count' },
        { op: 'sum', field: score },
        { op: 'unique', field: flag, accuracy: 'approximate' },
      ],
      group_by: [flag, { kind: 'system', key: 'operation_id' }],
      interval: 'hour',
      limit: 100,
      filter: {
        op: 'and',
        args: [
          { op: 'eq', field: flag, value: false },
          {
            op: 'or',
            args: [
              { op: 'eq', field: score, value: 0 },
              { op: 'in', field: score, value: [0, 2, 0] },
            ],
          },
          { op: 'eq', field: { kind: 'system', key: 'uid' }, value: '' },
        ],
      },
    },
  };
}
function funnel(): SavedAnalysisDefinition {
  return {
    title: '转化分析',
    description: '',
    visualization: { kind: 'funnel' },
    query: {
      kind: 'funnel',
      subject: 'uid',
      limit: 100,
      filter: { op: 'eq', field: flag, value: false },
      funnel: {
        mode: 'ordered_with_interleaving',
        window_seconds: 3600,
        steps: [
          {
            event_key: 'activity_view',
            schema_version: 2,
            filter: { op: 'in', field: score, value: [0, 1, 0] },
          },
          { event_key: 'activity_complete', schema_version: 3 },
        ],
        same_entity: [
          { step_index: 0, field: score },
          { step_index: 1, field: { kind: 'system', key: 'operation_id' } },
        ],
      },
    },
  };
}

test('saved configuration starts with an explicit empty version choice and retains every advanced value across edits', () => {
  const initial = initialSavedAnalysisDefinition();
  expect(initial.query.event_versions).toEqual([]);
  expect(savedAnalysisIssues(initial).map((issue) => issue.path)).toEqual([
    '/title',
    '/query/event_versions',
  ]);
  const original = definition(),
    before = structuredClone(original);
  const metadata = { ...original, title: '修改标题', description: '修改描述' };
  const next = patchSavedQuery(metadata, { limit: 200 });
  expect(next.query).toEqual({ ...before.query, limit: 200 });
  expect(next.query.metrics).toHaveLength(3);
  expect(next.query.group_by).toEqual(before.query.group_by);
  expect(next.query.filter).toEqual(before.query.filter);
  expect(original).toEqual(before);
  expect(parseSavedAnalysisDefinition(JSON.stringify(next))).toEqual(next);
  const clone = cloneSavedDefinition(original);
  clone.query.event_versions![0]!.schema_version = 4;
  expect(original.query.event_versions![0]!.schema_version).toBe(2);
});

test('saved funnel window edits preserve ordered steps, step filters, entity links and top-level filters', () => {
  const original = funnel(),
    before = structuredClone(original);
  const next = patchSavedQuery(original, {
    funnel: { ...original.query.funnel!, window_seconds: 7200 },
  });
  expect(next.query.funnel!.steps).toEqual(before.query.funnel!.steps);
  expect(next.query.funnel!.same_entity).toEqual(before.query.funnel!.same_entity);
  expect(next.query.filter).toEqual(before.query.filter);
  expect(savedAnalysisIssues(next)).toEqual([]);
  expect(parseSavedAnalysisDefinition(JSON.stringify(next))).toEqual(next);
  expect(original).toEqual(before);
  expect(savedPins(next.query)).toEqual([
    { event_key: 'activity_view', schema_version: 2 },
    { event_key: 'activity_complete', schema_version: 3 },
  ]);
});

test('saved typed filter values preserve false, zero, empty strings and ordered arrays without coercing quoted scalars', () => {
  for (const [type, op, text, expected] of [
    ['boolean', 'eq', 'false', false],
    ['number', 'eq', '0', 0],
    ['string', 'eq', '', ''],
    ['array:string', 'eq', '[]', []],
    ['array:string', 'eq', '["", "x", ""]', ['', 'x', '']],
    ['array:boolean', 'eq', '[false,true,false]', [false, true, false]],
    ['array:integer', 'eq', '[0, 2, 0]', [0, 2, 0]],
    ['array:string', 'contains', '', ''],
    ['string', 'in', '["false", "0"]', ['false', '0']],
  ] as const)
    expect(parseSavedFilterValue(text, type, op)).toEqual(expected);
  for (const [type, op, text] of [
    ['boolean', 'eq', '0'],
    ['array:boolean', 'eq', '["false"]'],
    ['number', 'eq', ''],
    ['array:integer', 'eq', '[1.5]'],
    ['array:integer', 'eq', '["1"]'],
    ['string', 'gt', 'x'],
    ['number', 'eq', '9007199254740992'],
    ['array:number', 'eq', '[1e999]'],
    ['array:string', 'eq', JSON.stringify(Array(21).fill('x'))],
    ['datetime', 'eq', '2026-11-09T00:00:00'],
  ])
    expect(() => parseSavedFilterValue(text!, type!, op!)).toThrow();
  expect(savedFilterText({ op: 'eq', field: flag, value: false })).toBe('false');
  expect(savedFilterText({ op: 'eq', field: score, value: 0 })).toBe('0');
  expect(savedFilterText({ op: 'eq', field: score, value: [0, 1, 0] })).toBe('[0,1,0]');
});

test('saved structured filter projection refuses nested trees and never flattens advanced semantics', () => {
  expect(flatSavedFilters(definition().query.filter)).toBeNull();
  const leaf = { op: 'eq' as const, field: flag, value: false };
  expect(flatSavedFilters(leaf)).toEqual({ join: 'and', rows: [leaf] });
  const group = { op: 'or' as const, args: [leaf, { op: 'eq' as const, field: score, value: 0 }] };
  expect(flatSavedFilters(group)).toEqual({ join: 'or', rows: group.args });
  expect(flatSavedFilters(undefined)).toEqual({ join: 'and', rows: [] });
});

test('saved advanced JSON rejects runtime context, resolved types, unknown keys, duplicate keys and unsafe integers', () => {
  for (const [key, value] of Object.entries({
    environment: 'development',
    time_range: { from: '2026-11-09T00:00:00Z', to: '2026-11-09T01:00:00Z' },
    timezone: 'UTC',
    identity_filter: { uid: 'viewer' },
    identity_revision: 1,
    event_keys: ['activity_view'],
    cursor: 'cursor',
    resolved_type: 'number',
  })) {
    const input = definition();
    Object.assign(input.query, { [key]: value });
    expect(() => parseSavedAnalysisDefinition(JSON.stringify(input)), key).toThrow('不支持此字段');
  }
  const unknown = cloneSavedDefinition(definition());
  Object.assign(unknown.query.metrics![1]!.field!, { resolved_type: 'number' });
  expect(() => parseSavedAnalysisDefinition(JSON.stringify(unknown))).toThrow('resolved_type');
  const typo = definition();
  Object.assign(typo.visualization, { options: { script: 'x' } });
  expect(() => parseSavedAnalysisDefinition(JSON.stringify(typo))).toThrow('options');
  for (const target of ['metric', 'visualization', 'filter']) {
    const malformed = cloneSavedDefinition(definition());
    if (target === 'metric') Object.assign(malformed.query.metrics![1]!, { op: 'constructor' });
    if (target === 'visualization') Object.assign(malformed.visualization, { kind: 'toString' });
    if (target === 'filter')
      malformed.query.filter = { ...malformed.query.filter!, op: 'constructor' } as never;
    expect(() => parseSavedAnalysisDefinition(JSON.stringify(malformed)), target).toThrow();
  }
  expect(() => parseSavedJSON('{"title":"a","title":"b"}')).toThrow('重复');
  expect(() => parseSavedJSON('{"query":{"kind":"trend","\\u006bind":"funnel"}}')).toThrow('重复');
  expect(() =>
    parseSavedJSON('{"title":"commas, braces{} and \\\"quotes\\\"","query":{}}'),
  ).not.toThrow();
  expect(() => parseSavedJSON('{"value":9007199254740993}')).toThrow('安全');
  expect(() => parseSavedJSON('{"value":1e999}')).toThrow('安全');
  expect(() => parseSavedJSON('{"value":"' + 'x'.repeat(128 * 1024) + '"}')).toThrow('128 KiB');
});

test('saved property references must stay inside exact selected facts and entity fields inside their own step', () => {
  const changed = patchSavedQuery(definition(), {
    event_versions: [{ event_key: 'activity_view', schema_version: 3 }],
  });
  expect(
    savedAnalysisIssues(changed).filter((issue) => issue.message.includes('固定版本')).length,
  ).toBeGreaterThan(3);
  expect(changed.query.metrics![1]!.field).toEqual(score);
  const duplicate = patchSavedQuery(definition(), {
    event_versions: [
      { event_key: 'activity_view', schema_version: 2 },
      { event_key: 'activity_view', schema_version: 3 },
    ],
  });
  expect(
    savedAnalysisIssues(duplicate).some((issue) => issue.message.includes('只能固定一个版本')),
  ).toBe(true);
  const invalid = funnel();
  invalid.query.funnel!.same_entity![0]!.step_index = 1;
  expect(savedAnalysisIssues(invalid).some((issue) => issue.message.includes('对应步骤'))).toBe(
    true,
  );
  const unsafe = definition();
  unsafe.query.event_versions![0]!.schema_version = Number.MAX_SAFE_INTEGER + 1;
  expect(savedAnalysisIssues(unsafe).some((issue) => issue.path.endsWith('/schema_version'))).toBe(
    true,
  );
});

test('saved visualization compatibility accounts for all metrics, grouping and intervals without rewriting query configuration', () => {
  const multi = definition().query;
  expect(savedVisualizationCompatible(multi, 'metric')).toBe(false);
  expect(savedVisualizationCompatible(multi, 'line')).toBe(true);
  expect(savedVisualizationCompatible(multi, 'bar')).toBe(true);
  expect(savedVisualizationCompatible(multi, 'funnel')).toBe(false);
  expect(savedVisualizationCompatible(multi, 'table')).toBe(true);
  const metric = {
    kind: 'trend' as const,
    event_versions: definition().query.event_versions,
    metrics: [{ op: 'count' as const }],
  };
  expect(savedVisualizationCompatible(metric, 'metric')).toBe(true);
  expect(savedVisualizationCompatible(metric, 'line')).toBe(false);
  expect(savedVisualizationCompatible(metric, 'bar')).toBe(false);
  expect(savedVisualizationCompatible({ ...metric, group_by: [flag] }, 'bar')).toBe(true);
  expect(savedVisualizationCompatible(funnel().query, 'funnel')).toBe(true);
  const incompatible = { ...definition(), visualization: { kind: 'metric' as const } };
  expect(
    savedAnalysisIssues(incompatible).some((issue) => issue.path === '/visualization/kind'),
  ).toBe(true);
  const before = definition();
  const replaced = replaceSavedKind(before, 'funnel');
  expect(replaced.title).toBe(before.title);
  expect(replaced.description).toBe(before.description);
  expect(replaced.query.filter).toBeUndefined();
  expect(replaced.query.metrics).toBeUndefined();
  expect(replaced.query.funnel!.steps).toHaveLength(2);
  expect(before.query.kind).toBe('trend');
});

const metricRef: MetricReference = {
  metric_id: '11111111-1111-4111-8111-111111111111',
  version: 2,
};

test('metric reference source switches cleanly and keeps inline definitions byte-stable', () => {
  const inline = initialSavedAnalysisDefinition();
  expect(isMetricReferenceDefinition(inline)).toBe(false);

  const metricMode = switchSavedAnalysisSource(inline, 'metric');
  expect(isMetricReferenceDefinition(metricMode)).toBe(true);
  expect(metricMode.metric_ref).toEqual(emptyMetricReference());
  expect(metricMode.visualization.kind).toBe('metric');
  // The inline original is not mutated.
  expect(isMetricReferenceDefinition(inline)).toBe(false);
  expect(inline.metric_ref).toBeUndefined();

  const backToInline = switchSavedAnalysisSource(metricMode, 'inline');
  expect(isMetricReferenceDefinition(backToInline)).toBe(false);
  expect(backToInline.metric_ref).toBeUndefined();

  // Switching an already-metric definition is an idempotent clone.
  expect(switchSavedAnalysisSource(metricMode, 'metric')).toEqual(metricMode);
});

test('metric reference validation only checks the pin and metric card', () => {
  const base = {
    ...switchSavedAnalysisSource(initialSavedAnalysisDefinition(), 'metric'),
    title: '指标卡片分析',
  };
  // Unselected sentinel is invalid and points at the metric id/version.
  const unselected = savedAnalysisIssues(base);
  expect(unselected.some((i) => i.path === '/metric_ref/metric_id')).toBe(true);
  expect(unselected.some((i) => i.path === '/metric_ref/version')).toBe(true);

  const picked = patchMetricReference(base, metricRef);
  expect(savedAnalysisIssues(picked)).toHaveLength(0);
  expect(validMetricReference(picked.metric_ref)).toBe(true);

  // A non-metric visualization is rejected even with a valid pin.
  const wrongCard = { ...picked, visualization: { kind: 'line' as const } };
  expect(savedAnalysisIssues(wrongCard).some((i) => i.path === '/visualization/kind')).toBe(true);

  // Inline query requirements are not applied to a metric-backed definition.
  const emptyEvents = cloneSavedDefinition(picked);
  emptyEvents.query.event_versions = [];
  expect(savedAnalysisIssues(emptyEvents)).toHaveLength(0);

  expect(validMetricReference({ metric_id: 'not-uuid', version: 1 })).toBe(false);
  expect(validMetricReference({ metric_id: metricRef.metric_id, version: 0 })).toBe(false);
});

test('metric reference write input omits the inline query', () => {
  const picked = patchMetricReference(
    switchSavedAnalysisSource(initialSavedAnalysisDefinition(), 'metric'),
    metricRef,
  );
  const input = toSavedAnalysisInput(picked);
  expect(input.metric_ref).toEqual(metricRef);
  expect(input.query).toBeUndefined();
  expect(input.visualization.kind).toBe('metric');

  // Inline analyses keep the full definition and never carry a pin.
  const inline = definition();
  const inlineInput = toSavedAnalysisInput(inline);
  expect(inlineInput.query).toEqual(inline.query);
  expect(inlineInput.metric_ref).toBeUndefined();
});

test('metric reference summary describes the pinned version', () => {
  const empty = switchSavedAnalysisSource(initialSavedAnalysisDefinition(), 'metric');
  expect(savedDefinitionSummary(empty)).toContain('请选择已发布指标');
  const picked = patchMetricReference(empty, metricRef);
  const summary = savedDefinitionSummary(picked);
  expect(summary).toContain(metricRef.metric_id);
  expect(summary).toContain('v2');
});

test('saved analysis search terms trim blanks and clamp to the server rune limit', () => {
  expect(normalizeSavedAnalysisSearch('  留存 分析 ')).toBe('留存 分析');
  expect(normalizeSavedAnalysisSearch('   ')).toBe('');
  expect(normalizeSavedAnalysisSearch('')).toBe('');
  const atLimit = '字'.repeat(maxSavedAnalysisSearchRunes);
  expect(normalizeSavedAnalysisSearch(atLimit)).toBe(atLimit);
  const overLimit = '字'.repeat(maxSavedAnalysisSearchRunes + 1);
  const clamped = normalizeSavedAnalysisSearch(overLimit);
  expect([...clamped].length).toBe(maxSavedAnalysisSearchRunes);
  expect(clamped).toBe(atLimit);
  const emojiClamped = normalizeSavedAnalysisSearch(
    ` ${'😀'.repeat(maxSavedAnalysisSearchRunes + 1)} `,
  );
  expect([...emojiClamped].length).toBe(maxSavedAnalysisSearchRunes);
  expect(emojiClamped.startsWith('😀')).toBe(true);
  expect(normalizeSavedAnalysisSearch('a\tb  c')).toBe('a\tb  c');
});

test('saved trend accepts the week interval but keeps the closed set', () => {
  const weekly = definition();
  weekly.query.interval = 'week';
  expect(savedAnalysisIssues(weekly).some((issue) => issue.path === '/query/interval')).toBe(false);
  const invalid = definition();
  invalid.query.interval = 'month' as never;
  expect(savedAnalysisIssues(invalid).some((issue) => issue.path === '/query/interval')).toBe(true);
});
