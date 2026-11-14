import { expect, test } from '@playwright/test';
import {
  buildRequest,
  AnalysisValidationError,
  validateAnalysisSection,
  initialForm,
  metricLabel,
  publishedFields,
  type AnalysisForm,
  type QueryField,
} from '../../apps/console/src/analysis-model';

function form(): AnalysisForm {
  return { ...initialForm('UTC'), from: '2026-11-01T00:00', to: '2026-11-02T00:00' };
}
function filterValue(type: string, op: string, value: string) {
  const input = form();
  const reference: QueryField = {
    kind: 'property',
    event_key: 'page_opened',
    schema_version: 2,
    key: 'values',
  };
  input.filters = [
    {
      id: 1,
      field: 'page_opened:2:values',
      op,
      value,
      property: { id: 'page_opened:2:values', label: 'values', type, reference },
    },
  ];
  const filter = buildRequest(input, 'development').filter;
  if (!filter || !('args' in filter) || !filter.args[0] || !('value' in filter.args[0]))
    throw new Error('Expected comparison filter');
  return filter.args[0].value;
}

test('analysis count and explicit page-event PV preserve event scope and UV semantics', () => {
  const input = form();
  const count = buildRequest(input, 'development');
  expect(count.metrics).toEqual([{ op: 'count' }]);
  expect(count.event_keys).toBeUndefined();
  expect(metricLabel(count)).toBe('事件数');
  input.metric = 'pv';
  expect(() => buildRequest(input, 'development')).toThrow('请选择代表页面访问的已发布事件');
  input.event = 'page_opened';
  const pv = buildRequest(input, 'staging');
  expect(pv.event_keys).toEqual(['page_opened']);
  expect(pv.metrics).toEqual([{ op: 'count' }]);
  expect(pv.environment).toBe('staging');
  expect(metricLabel(pv, 'pv')).toBe('PV');
  expect(metricLabel(pv, 'count')).toBe('事件数');
  input.event = 'order_completed';
  expect(pv.event_keys).toEqual(['page_opened']);
  input.metric = 'uv';
  input.subject = 'anonymous';
  input.accuracy = 'approximate';
  const uv = buildRequest(input, 'production');
  expect(uv.metrics).toEqual([{ op: 'unique', accuracy: 'approximate' }]);
  expect(uv.subject).toBe('anonymous');
  expect(metricLabel(uv, 'uv')).toBe('匿名标识 UV');
  input.subject = 'uid';
  input.accuracy = 'exact';
  expect(buildRequest(input, 'production').metrics).toEqual([{ op: 'unique', accuracy: 'exact' }]);
});

test('analysis validation identifies the editable section without unrelated errors blocking it', () => {
  const input = form();
  input.mode = 'journey';
  input.subject = 'anonymous';
  input.identity = 'not-a-uuid';
  expect(() => validateAnalysisSection(input, 'range')).not.toThrow();
  expect(() => buildRequest(input, 'development')).toThrow(AnalysisValidationError);
  try {
    buildRequest(input, 'development');
  } catch (error) {
    expect(error).toMatchObject({ target: 'identity' });
  }
  input.to = input.from;
  expect(() => validateAnalysisSection(input, 'range')).toThrow('开始早于结束');
  expect(() => validateAnalysisSection(input, 'settings')).not.toThrow();
  input.limit = '501';
  expect(() => validateAnalysisSection(input, 'settings')).toThrow('结果行数');
  input.filters = [{ id: 1, field: 'uid', op: 'in', value: '[' }];
  expect(() => validateAnalysisSection(input, 'filters')).toThrow('JSON 数组');
  input.steps = [{ event: 'page_opened', version: '1' }];
  expect(() => validateAnalysisSection(input, 'funnel')).toThrow();
});

test('analysis typed filters preserve empty strings, zero, false and JSON arrays without coercion', () => {
  for (const [type, op, value, expected] of [
    ['array:string', 'eq', '[]', []],
    ['array:string', 'neq', '[""]', ['']],
    ['array:string', 'eq', '["", "x", ""]', ['', 'x', '']],
    ['array:integer', 'eq', '[0, 1, -2]', [0, 1, -2]],
    ['array:boolean', 'eq', '[false, true]', [false, true]],
    ['string', 'in', '[""]', ['']],
    ['string', 'eq', '', ''],
    ['array:string', 'contains', '', ''],
    ['number', 'eq', '0', 0],
    ['boolean', 'eq', 'false', false],
  ] as const)
    expect(filterValue(type, op, value)).toEqual(expected);
  for (const [type, op, value] of [
    ['array:integer', 'eq', '["1"]'],
    ['array:integer', 'eq', '[1.5]'],
    ['array:boolean', 'eq', '["false"]'],
    ['array:string', 'eq', '[null]'],
    ['array:string', 'eq', '{}'],
    ['array:string', 'eq', '['],
    ['array:string', 'eq', JSON.stringify(Array(21).fill('x'))],
    ['array:string', 'eq', JSON.stringify(['x'.repeat(1025)])],
    ['string', 'gt', 'x'],
  ])
    expect(() => filterValue(type!, op!, value!)).toThrow();
});

test('retention builds a uid-only cohort request and closes the validation surface', () => {
  const input = form();
  input.mode = 'retention';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-08T00:00';
  input.retentionStart = { event: 'sign_up', version: '1' };
  input.retentionReturn = { event: 'login', version: '2' };
  input.retentionGranularity = 'day';
  input.retentionPeriods = '7';
  const request = buildRequest(input, 'development');
  expect(request.kind).toBe('retention');
  expect(request.subject).toBe('uid');
  expect(request.event_keys).toBeUndefined();
  expect(request.metrics).toBeUndefined();
  expect(request.group_by).toBeUndefined();
  expect(request.interval).toBeUndefined();
  expect(request.retention).toEqual({
    start_event: { event_key: 'sign_up', schema_version: 1 },
    return_event: { event_key: 'login', schema_version: 2 },
    granularity: 'day',
    periods: 7,
  });

  // Missing pinned events fail on the retention editor section, not range.
  const missingEvents = { ...input, retentionStart: { event: '', version: '' } };
  expect(() => buildRequest(missingEvents, 'development')).toThrow('起始事件');
  try {
    buildRequest(missingEvents, 'development');
  } catch (error) {
    expect(error).toMatchObject({ target: 'retention' });
  }

  // Periods are a 1..12 closed set.
  for (const periods of ['0', '13', '1.5', '']) {
    const bad = { ...input, retentionPeriods: periods };
    expect(() => buildRequest(bad, 'development')).toThrow(AnalysisValidationError);
  }

  // Section validation isolates retention errors from the other editors.
  expect(() => validateAnalysisSection(input, 'retention')).not.toThrow();
  const badGranularity = { ...input, retentionGranularity: 'hour' as 'day' };
  expect(() => validateAnalysisSection(badGranularity, 'retention')).toThrow();
});

test('retention time window follows periods multiplied by granularity', () => {
  const input = form();
  input.mode = 'retention';
  input.retentionStart = { event: 'sign_up', version: '1' };
  input.retentionReturn = { event: 'login', version: '1' };
  input.retentionGranularity = 'day';
  input.retentionPeriods = '7';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-09T00:00'; // 8 days exceeds a 7-period day matrix
  expect(() => buildRequest(input, 'development')).toThrow('最多 7 天');

  // A week matrix permits up to periods × 7 days.
  input.retentionGranularity = 'week';
  input.retentionPeriods = '2';
  input.to = '2026-11-15T00:00'; // 14 days, exactly the two-week window
  const weekly = buildRequest(input, 'development');
  expect(weekly.retention?.granularity).toBe('week');
  expect(weekly.retention?.periods).toBe(2);

  input.to = '2026-11-16T00:00'; // 15 days exceeds the 14-day window
  expect(() => buildRequest(input, 'development')).toThrow('最多 14 天');

  // The 84-day hard ceiling: 12 weekly periods are allowed, 12 day periods are not 84 days.
  input.retentionGranularity = 'week';
  input.retentionPeriods = '12';
  input.from = '2026-11-01T00:00';
  input.to = '2027-02-01T00:00'; // 92 days, beyond 84
  expect(() => buildRequest(input, 'development')).toThrow('最多 84 天');
});

test('analysis preserves immutable property versions, funnel order, timezone and bounded half-open ranges', () => {
  const fields = publishedFields(
    {
      properties: {
        price: { type: 'number' },
        flags: { type: 'array', items: { type: 'boolean' } },
        nested: { type: 'object' },
      },
    },
    'checkout_opened',
    3,
  );
  expect(fields.map((field) => field.type)).toEqual(['number', 'array:boolean']);
  const input = form();
  input.event = 'checkout_opened';
  input.timezone = 'America/New_York';
  input.filters = [{ id: 1, field: fields[0]!.id, property: fields[0]!, op: 'gte', value: '0' }];
  const request = buildRequest(input, 'staging');
  expect(request.time_range).toEqual({
    from: '2026-11-01T00:00:00.000Z',
    to: '2026-11-02T00:00:00.000Z',
  });
  expect(request.timezone).toBe('America/New_York');
  expect(request.filter).toEqual({
    op: 'and',
    args: [
      {
        op: 'gte',
        field: { kind: 'property', event_key: 'checkout_opened', schema_version: 3, key: 'price' },
        value: 0,
      },
    ],
  });
  input.mode = 'funnel';
  input.subject = 'uid';
  input.steps = [
    { event: 'checkout_opened', version: '3' },
    { event: 'order_completed', version: '2' },
  ];
  expect(buildRequest(input, 'staging').funnel).toMatchObject({
    mode: 'ordered_with_interleaving',
    steps: [
      { event_key: 'checkout_opened', schema_version: 3 },
      { event_key: 'order_completed', schema_version: 2 },
    ],
  });
  input.to = '2026-11-09T00:00';
  expect(() => buildRequest(input, 'staging')).toThrow('最多 7 天');
  input.mode = 'metrics';
  input.to = '2026-12-03T00:00';
  expect(() => buildRequest(input, 'staging')).toThrow('最多 31 天');
  input.mode = 'trend';
  input.to = '2026-11-02T00:00';
  input.interval = 'minute';
  expect(() => buildRequest(input, 'staging')).toThrow('当前粒度可能超出结果行数');
  input.interval = 'hour';
  input.limit = '25';
  expect(buildRequest(input, 'staging').interval).toBe('hour');
  input.to = input.from;
  expect(() => buildRequest(input, 'staging')).toThrow('开始早于结束');
});
