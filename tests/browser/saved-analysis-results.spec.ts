import { expect, test } from '@playwright/test';
import type {
  QueryResult,
  SavedAnalysisDefinition,
  SavedAnalysisExecute,
  SavedAnalysisVersion,
} from '../../packages/api-client/src/index';
import { savedAnalysisResultChart } from '../../apps/console/src/saved-analysis-run/result-chart';

const execution: SavedAnalysisExecute = {
  time_range: { from: '2026-11-09T10:15:00Z', to: '2026-11-09T12:00:00Z' },
  timezone: 'Asia/Shanghai',
};
const flag = {
  kind: 'property' as const,
  event_key: 'activity_opened',
  schema_version: 2,
  key: 'enabled',
};
function version(): SavedAnalysisVersion {
  return {
    analysis_id: '00000000-0000-4000-8000-000000000001',
    project_id: '00000000-0000-4000-8000-000000000002',
    environment: 'development',
    version: 7,
    draft_revision: 9,
    created_at: '2026-11-09T10:00:00Z',
    created_by: '00000000-0000-4000-8000-000000000003',
    definition_hash: 'a'.repeat(64),
    dependency_hash: 'b'.repeat(64),
    validation_hash: 'c'.repeat(64),
    dependencies: {
      project_id: '00000000-0000-4000-8000-000000000002',
      environment: 'development',
      selection_mode: 'event_versions',
      unfixed_event_keys: [],
      event_versions: [
        { event_key: 'activity_opened', schema_version: 2, definition_hash: 'd'.repeat(64) },
      ],
    },
    definition: {
      title: '已提交的分析',
      description: '',
      visualization: { kind: 'line' },
      query: {
        kind: 'trend',
        event_versions: [{ event_key: 'activity_opened', schema_version: 2 }],
        group_by: [flag],
        interval: 'hour',
        metrics: [{ op: 'count' }, { op: 'unique', field: flag, accuracy: 'approximate' }],
      },
    },
  };
}
function result(rows: QueryResult['rows'], columns: QueryResult['columns']): QueryResult {
  return {
    query_id: '00000000-0000-4000-8000-000000000004',
    rows,
    columns,
    metadata: {
      data_watermark: null,
      accuracy: 'approximate',
      sample_rate: 1,
      coverage: { time_range: execution.time_range, state: 'partial', known_gaps: [] },
      cache_hit: false,
      generated_at: '2026-11-09T12:00:01Z',
    },
  };
}
const groupedColumns = [
  { key: 'group_0', type: 'boolean' },
  { key: 'group_0_present', type: 'boolean' },
  { key: 'metric_0', type: 'number' },
  { key: 'metric_1', type: 'number' },
];

test('sparse grouped trends preserve every metric, numerical precision and missing buckets', () => {
  const submitted = version();
  const response = result(
    [
      {
        bucket: '2026-11-09T11:00:00Z',
        group_0: false,
        group_0_present: true,
        metric_0: 0,
        metric_1: null,
      },
      {
        bucket: '2026-11-09T10:00:00Z',
        group_0: true,
        group_0_present: true,
        metric_0: 3,
        metric_1: 0.12345678901234568,
      },
      {
        bucket: '2026-11-09T10:00:00Z',
        group_0: false,
        group_0_present: true,
        metric_0: 2,
        metric_1: 1.0000000000000002,
      },
    ],
    [{ key: 'bucket', type: 'datetime' }, ...groupedColumns],
  );
  const original = structuredClone({ submitted, response, execution });
  const chart = savedAnalysisResultChart(submitted, response, execution)!;
  expect(chart.categories.map((category) => category.key)).toEqual([
    '2026-11-09T10:00:00Z',
    '2026-11-09T11:00:00Z',
  ]);
  expect(chart.series.map((series) => series.values)).toEqual([
    [2, 0],
    [1.0000000000000002, null],
    [3, null],
    [0.12345678901234568, null],
  ]);
  expect(new Set(chart.series.map((series) => series.id)).size).toBe(4);
  expect(chart.series[0]?.label).toContain('false，事件数');
  expect(chart.series[1]?.label).toContain('去重值数');
  expect(chart.series[1]?.label).toContain('近似');
  expect(chart.series[1]?.unit).toBeUndefined();
  expect({ submitted, response, execution }).toEqual(original);
});

test('bar groups distinguish missing, null, false and numeric zero without coercion', () => {
  const submitted = version();
  submitted.definition.visualization.kind = 'bar';
  delete submitted.definition.query.interval;
  const response = result(
    [
      { group_0_present: false, metric_0: null, metric_1: 0 },
      { group_0: null, group_0_present: true, metric_0: 0, metric_1: 2 },
      { group_0: false, group_0_present: true, metric_0: 1, metric_1: 3 },
      { group_0: 0, group_0_present: true, metric_0: 2, metric_1: 4 },
    ],
    groupedColumns,
  );
  const chart = savedAnalysisResultChart(submitted, response, execution)!;
  expect(chart.categories.map((category) => category.label)).toEqual([
    '缺失',
    'null',
    'false',
    '0',
  ]);
  expect(new Set(chart.categories.map((category) => category.key)).size).toBe(4);
  expect(chart.series.map((series) => series.values)).toEqual([
    [null, 0, 1, 2],
    [0, 2, 3, 4],
  ]);
});

test('partial first buckets retain their timestamp, submitted bounds, timezone and version label', () => {
  const submitted = version();
  const chart = savedAnalysisResultChart(
    submitted,
    result(
      [{ bucket: '2026-11-09T10:00:00Z', group_0: true, metric_0: 1 }],
      [{ key: 'bucket', type: 'datetime' }, ...groupedColumns],
    ),
    execution,
  )!;
  expect(chart.label).toBe('已提交的分析');
  expect(chart.timezone).toBe('Asia/Shanghai');
  expect(chart.timeRange).toEqual({
    from: Date.parse(execution.time_range.from),
    to: Date.parse(execution.time_range.to),
  });
  expect(chart.intervalMs).toBe(3600000);
  expect(chart.categories[0]?.timestamp).toBe(Date.parse('2026-11-09T10:00:00Z'));
  expect(chart.categories[0]!.timestamp!).toBeLessThan(chart.timeRange!.from);
  expect(chart.categories[0]?.label).toContain('18:00:00');
});

test('chart values never turn numeric strings, booleans, absent or nonfinite values into zero', () => {
  const submitted = version();
  submitted.definition.visualization.kind = 'bar';
  delete submitted.definition.query.interval;
  const values = [0, -0.0000000000000002, '12', false, undefined, null, Infinity, NaN];
  const chart = savedAnalysisResultChart(
    submitted,
    result(
      values.map((value, index) => ({
        group_0: index,
        metric_0: value,
      })),
      groupedColumns,
    ),
    execution,
  )!;
  expect(chart.series[0]?.values).toEqual([
    0,
    -0.0000000000000002,
    null,
    null,
    null,
    null,
    null,
    null,
  ]);
});

test('funnel ordering and labels use pinned submitted steps while preserving raw response order', () => {
  const submitted = version();
  submitted.definition.visualization.kind = 'funnel';
  submitted.definition.query = {
    kind: 'funnel',
    subject: 'uid',
    funnel: {
      mode: 'ordered_with_interleaving',
      window_seconds: 3600,
      steps: [
        { event_key: 'activity_opened', schema_version: 2 },
        { event_key: 'activity_finished', schema_version: 3 },
      ],
    },
  };
  const response = result(
    [
      { step_index: 1, event_key: 'changed_label', subjects: 0 },
      { step_index: 0, event_key: 'changed_label', subjects: 9007199254740991 },
    ],
    [
      { key: 'step_index', type: 'integer' },
      { key: 'subjects', type: 'integer' },
    ],
  );
  const chart = savedAnalysisResultChart(submitted, response, execution)!;
  expect(chart.categories.map((category) => category.label)).toEqual([
    '1. activity_opened v2',
    '2. activity_finished v3',
  ]);
  expect(chart.series[0]?.values).toEqual([9007199254740991, 0]);
  expect(response.rows.map((row) => row.step_index)).toEqual([1, 0]);
});

test('metric and table visualizations leave the complete result to their existing presentation', () => {
  const submitted = version();
  const response = result([], groupedColumns);
  for (const kind of [
    'metric',
    'table',
  ] satisfies SavedAnalysisDefinition['visualization']['kind'][]) {
    submitted.definition.visualization.kind = kind;
    expect(savedAnalysisResultChart(submitted, response, execution)).toBeUndefined();
  }
});
