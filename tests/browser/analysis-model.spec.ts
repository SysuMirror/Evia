import { expect, test } from '@playwright/test';
import {
  buildRequest,
  AnalysisValidationError,
  validateAnalysisSection,
  initialForm,
  metricLabel,
  publishedFields,
  fieldOperators,
  operators,
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
    ['array:string', 'not_contains', '', ''],
    ['string', 'not_in', '["a", "b"]', ['a', 'b']],
    ['string', 'not_contains', 'x', 'x'],
    ['string', 'not_prefix', 'home', 'home'],
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
    ['string', 'not_in', 'x'],
  ])
    expect(() => filterValue(type!, op!, value!)).toThrow();
});

test('retention builds uid or anonymous cohort requests and closes the validation surface', () => {
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

  // The same form with the anonymous subject selects anonymous_id downstream.
  input.subject = 'anonymous';
  const anonymousRequest = buildRequest(input, 'development');
  expect(anonymousRequest.subject).toBe('anonymous');
  expect(anonymousRequest.retention).toEqual(request.retention);
  input.subject = 'uid';

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

test('retention natural-month granularity passes through with a 31-day per-period window', () => {
  const input = form();
  input.mode = 'retention';
  input.retentionStart = { event: 'sign_up', version: '1' };
  input.retentionReturn = { event: 'login', version: '1' };
  input.retentionGranularity = 'month';
  input.retentionPeriods = '2';
  input.from = '2026-11-01T00:00';
  input.to = '2027-01-02T00:00'; // 62 days = two 31-day month windows
  const monthly = buildRequest(input, 'development');
  expect(monthly.retention?.granularity).toBe('month');
  expect(monthly.retention?.periods).toBe(2);

  input.to = '2027-01-03T00:00'; // 63 days exceeds the 62-day month window
  expect(() => buildRequest(input, 'development')).toThrow('最多 62 天');

  // Twelve month periods permit the conservative 372-day scan ceiling.
  input.retentionPeriods = '12';
  input.from = '2026-11-01T00:00';
  input.to = '2027-11-08T00:00'; // 372 days
  const yearly = buildRequest(input, 'development');
  expect(yearly.retention?.granularity).toBe('month');
  expect(yearly.retention?.periods).toBe(12);

  // Section validation accepts month and still rejects a foreign granularity.
  expect(() => validateAnalysisSection(input, 'retention')).not.toThrow();
  const bad = { ...input, retentionGranularity: 'hour' as 'day' };
  expect(() => validateAnalysisSection(bad, 'retention')).toThrow();
});

test('segmentation builds unique-subject group requests and closes the validation surface', () => {
  const scalarField = publishedFields(
    {
      properties: { plan: { type: 'string' }, tags: { type: 'array', items: { type: 'string' } } },
    },
    'sign_up',
    1,
  ).find((field) => field.reference.key === 'plan')!;
  const input = form();
  input.mode = 'segment';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.segmentEvent = { event: 'sign_up', version: '1' };
  input.segmentField = scalarField.id;
  input.segmentProperty = scalarField;
  const request = buildRequest(input, 'development');
  expect(request.kind).toBe('segment');
  expect(request.subject).toBe('uid');
  expect(request.event_keys).toBeUndefined();
  expect(request.group_by).toBeUndefined();
  expect(request.metrics).toBeUndefined();
  expect(request.interval).toBeUndefined();
  expect(request.segment).toEqual({
    event: { event_key: 'sign_up', schema_version: 1 },
    field: { kind: 'property', event_key: 'sign_up', schema_version: 1, key: 'plan' },
  });

  // Anonymous subject is accepted and selects the same event scope.
  input.subject = 'anonymous';
  const anonymousRequest = buildRequest(input, 'production');
  expect(anonymousRequest.subject).toBe('anonymous');
  expect(anonymousRequest.environment).toBe('production');
  input.subject = 'uid';

  // Missing event/version or field fails on the segment editor section.
  const noEvent = { ...input, segmentEvent: { event: '', version: '' } };
  expect(() => buildRequest(noEvent, 'development')).toThrow('事件');
  try {
    buildRequest(noEvent, 'development');
  } catch (error) {
    expect(error).toMatchObject({ target: 'segment' });
  }
  const noField = { ...input, segmentField: '', segmentProperty: undefined };
  expect(() => buildRequest(noField, 'development')).toThrow(AnalysisValidationError);

  // Non-scalar (array) properties are rejected even when selected.
  const arrayField = publishedFields(
    { properties: { tags: { type: 'array', items: { type: 'string' } } } },
    'sign_up',
    1,
  )[0]!;
  const arrayInput = {
    ...input,
    segmentField: arrayField.id,
    segmentProperty: arrayField,
  };
  expect(() => buildRequest(arrayInput, 'development')).toThrow('标量');

  // Section validation isolates segment errors from other editors.
  expect(() => validateAnalysisSection(input, 'segment')).not.toThrow();
});

test('path analysis builds bounded ordered-sequence requests and closes the validation surface', () => {
  const input = form();
  input.mode = 'path';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.pathDepth = '3';
  // Without a start event the path begins at each subject's earliest event.
  const request = buildRequest(input, 'development');
  expect(request.kind).toBe('path');
  expect(request.subject).toBe('uid');
  expect(request.event_keys).toBeUndefined();
  expect(request.event_versions).toBeUndefined();
  expect(request.group_by).toBeUndefined();
  expect(request.metrics).toBeUndefined();
  expect(request.interval).toBeUndefined();
  expect(request.path).toEqual({ depth: 3 });

  // The optional pinned start event carries an immutable version.
  input.pathStart = { event: 'page_view', version: '2' };
  const pinned = buildRequest(input, 'development');
  expect(pinned.path).toEqual({
    depth: 3,
    start_event: { event_key: 'page_view', schema_version: 2 },
  });

  // Anonymous subject passes through and keeps the same path scope.
  input.subject = 'anonymous';
  const anonymousRequest = buildRequest(input, 'production');
  expect(anonymousRequest.subject).toBe('anonymous');
  expect(anonymousRequest.environment).toBe('production');
  expect(anonymousRequest.path).toEqual(pinned.path);
  input.subject = 'uid';

  // Depth is a 2..8 integer closed set.
  for (const depth of ['1', '9', '1.5', '0', '', '-1']) {
    const bad = { ...input, pathDepth: depth };
    expect(() => buildRequest(bad, 'development')).toThrow(AnalysisValidationError);
  }

  // A selected start event without a valid version fails closed on the path section.
  const badVersion = { ...input, pathStart: { event: 'page_view', version: '' } };
  expect(() => buildRequest(badVersion, 'development')).toThrow('版本');
  try {
    buildRequest(badVersion, 'development');
  } catch (error) {
    expect(error).toMatchObject({ target: 'path' });
  }

  // Section validation isolates path errors; an empty start is a valid choice.
  expect(() => validateAnalysisSection(input, 'path')).not.toThrow();
  const noStart = { ...input, pathStart: { event: '', version: '' } };
  expect(() => validateAnalysisSection(noStart, 'path')).not.toThrow();
});

test('path collapse_repeats is opt-in and omitted by default', () => {
  const input = form();
  input.mode = 'path';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.pathDepth = '3';

  // Default requests stay byte-stable: the optional flag is not emitted.
  const defaults = buildRequest(input, 'development');
  expect(defaults.path).toEqual({ depth: 3 });
  expect(defaults.path?.collapse_repeats).toBeUndefined();

  // Opting in emits the boolean while keeping the rest of the path shape.
  const collapsed = { ...input, pathCollapseRepeats: true };
  const request = buildRequest(collapsed, 'development');
  expect(request.path).toEqual({ depth: 3, collapse_repeats: true });

  // Section validation still passes with the flag enabled.
  expect(() => validateAnalysisSection(collapsed, 'path')).not.toThrow();
});

test('attribution builds last-touch credit requests and closes the validation surface', () => {
  const input = form();
  input.mode = 'attribution';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.attributionConversion = { event: 'purchase', version: '1' };
  input.attributionTouchpoints = [{ event: 'ad_click', version: '1' }];
  input.attributionWindow = '86400';
  const request = buildRequest(input, 'development');
  expect(request.kind).toBe('attribution');
  expect(request.subject).toBe('uid');
  expect(request.event_keys).toBeUndefined();
  expect(request.event_versions).toBeUndefined();
  expect(request.group_by).toBeUndefined();
  expect(request.metrics).toBeUndefined();
  expect(request.interval).toBeUndefined();
  expect(request.attribution).toEqual({
    conversion: { event_key: 'purchase', schema_version: 1 },
    touchpoints: [{ event_key: 'ad_click', schema_version: 1 }],
    window_seconds: 86400,
    model: 'last_touch',
  });

  // Multiple distinct touchpoints and the anonymous subject pass through.
  input.attributionTouchpoints = [
    { event: 'ad_click', version: '1' },
    { event: 'email_open', version: '2' },
  ];
  input.subject = 'anonymous';
  const anonymousRequest = buildRequest(input, 'production');
  expect(anonymousRequest.subject).toBe('anonymous');
  expect(anonymousRequest.environment).toBe('production');
  expect(anonymousRequest.attribution?.touchpoints).toHaveLength(2);
  input.subject = 'uid';

  // Missing conversion fails on the attribution editor section.
  const noConversion = { ...input, attributionConversion: { event: '', version: '' } };
  expect(() => buildRequest(noConversion, 'development')).toThrow('转化');
  try {
    buildRequest(noConversion, 'development');
  } catch (error) {
    expect(error).toMatchObject({ target: 'attribution' });
  }

  // A touchpoint equal to the conversion pair is rejected.
  const overlap = {
    ...input,
    attributionTouchpoints: [{ event: 'purchase', version: '1' }],
  };
  expect(() => buildRequest(overlap, 'development')).toThrow('转化事件');

  // Duplicate touchpoint pairs are rejected.
  const duplicate = {
    ...input,
    attributionTouchpoints: [
      { event: 'ad_click', version: '1' },
      { event: 'ad_click', version: '1' },
    ],
  };
  expect(() => buildRequest(duplicate, 'development')).toThrow('重复');

  // Window is a 1..604800 integer closed set.
  for (const windowValue of ['0', '604801', '1.5', '', '-1']) {
    const bad = { ...input, attributionWindow: windowValue };
    expect(() => buildRequest(bad, 'development')).toThrow(AnalysisValidationError);
  }

  // More than eight touchpoints exceed the closed set.
  const tooMany = {
    ...input,
    attributionTouchpoints: Array.from({ length: 9 }, (_, index) => ({
      event: `event_${index}`,
      version: '1',
    })),
  };
  expect(() => buildRequest(tooMany, 'development')).toThrow(AnalysisValidationError);

  // Section validation isolates attribution errors from the other editors.
  expect(() => validateAnalysisSection(input, 'attribution')).not.toThrow();
});

test('attribution model defaults to last-touch and passes first-touch through', () => {
  const input = form();
  input.mode = 'attribution';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.attributionConversion = { event: 'purchase', version: '1' };
  input.attributionTouchpoints = [{ event: 'ad_click', version: '1' }];
  input.attributionWindow = '86400';

  // initialForm selects last-touch, and the default request carries it.
  expect(input.attributionModel).toBe('last_touch');
  const defaults = buildRequest(input, 'development');
  expect(defaults.attribution?.model).toBe('last_touch');

  // Switching the form model flows through to the request without other changes.
  const firstTouch = { ...input, attributionModel: 'first_touch' as const };
  const request = buildRequest(firstTouch, 'development');
  expect(request.attribution).toEqual({
    conversion: { event_key: 'purchase', schema_version: 1 },
    touchpoints: [{ event_key: 'ad_click', schema_version: 1 }],
    window_seconds: 86400,
    model: 'first_touch',
  });
  expect(() => validateAnalysisSection(firstTouch, 'attribution')).not.toThrow();
});

test('attribution passes the linear equal-weight model through', () => {
  const input = form();
  input.mode = 'attribution';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.attributionConversion = { event: 'purchase', version: '1' };
  input.attributionTouchpoints = [
    { event: 'ad_click', version: '1' },
    { event: 'email_open', version: '1' },
  ];
  input.attributionWindow = '86400';
  input.attributionModel = 'linear';

  const request = buildRequest(input, 'development');
  expect(request.attribution).toEqual({
    conversion: { event_key: 'purchase', schema_version: 1 },
    touchpoints: [
      { event_key: 'ad_click', schema_version: 1 },
      { event_key: 'email_open', schema_version: 1 },
    ],
    window_seconds: 86400,
    model: 'linear',
  });
  expect(() => validateAnalysisSection(input, 'attribution')).not.toThrow();
});

test('attribution passes the position-based U-shape model through without a half-life', () => {
  const input = form();
  input.mode = 'attribution';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.attributionConversion = { event: 'purchase', version: '1' };
  input.attributionTouchpoints = [
    { event: 'ad_click', version: '1' },
    { event: 'email_open', version: '1' },
  ];
  input.attributionWindow = '86400';
  input.attributionModel = 'position_based';

  const request = buildRequest(input, 'development');
  expect(request.attribution).toEqual({
    conversion: { event_key: 'purchase', schema_version: 1 },
    touchpoints: [
      { event_key: 'ad_click', schema_version: 1 },
      { event_key: 'email_open', schema_version: 1 },
    ],
    window_seconds: 86400,
    model: 'position_based',
  });
  expect('decay_half_life_seconds' in (request.attribution ?? {})).toBe(false);
  expect(() => validateAnalysisSection(input, 'attribution')).not.toThrow();
});

test('attribution passes time_decay with a bounded half-life and rejects invalid ones', () => {
  const input = form();
  input.mode = 'attribution';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-05T00:00';
  input.attributionConversion = { event: 'purchase', version: '1' };
  input.attributionTouchpoints = [{ event: 'ad_click', version: '1' }];
  input.attributionWindow = '86400';
  input.attributionModel = 'time_decay';
  input.attributionHalfLife = '600';

  const request = buildRequest(input, 'development');
  expect(request.attribution).toEqual({
    conversion: { event_key: 'purchase', schema_version: 1 },
    touchpoints: [{ event_key: 'ad_click', schema_version: 1 }],
    window_seconds: 86400,
    model: 'time_decay',
    decay_half_life_seconds: 600,
  });
  expect(() => validateAnalysisSection(input, 'attribution')).not.toThrow();

  // Out-of-range and non-integer half-lives fail on the attribution section.
  for (const halfLife of ['0', '604801', '1.5', '']) {
    const bad = { ...input, attributionHalfLife: halfLife };
    expect(() => buildRequest(bad, 'development')).toThrow(AnalysisValidationError);
  }

  // Single-touch models omit the half-life entirely even when the form holds one.
  const singleTouch = { ...input, attributionModel: 'last_touch' as const };
  const singleRequest = buildRequest(singleTouch, 'development');
  expect(singleRequest.attribution?.decay_half_life_seconds).toBeUndefined();
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

test('field operators advertise negation members with labels per type', () => {
  expect(fieldOperators('title', 'string')).toEqual(
    expect.arrayContaining(['not_in', 'not_prefix', 'not_contains']),
  );
  expect(fieldOperators('score', 'number')).toEqual(expect.arrayContaining(['not_in']));
  expect(fieldOperators('score', 'number')).not.toContain('not_prefix');
  expect(fieldOperators('tags', 'array:string')).toEqual(expect.arrayContaining(['not_contains']));
  expect(fieldOperators('tags', 'array:string')).not.toContain('not_in');
  for (const op of ['not_in', 'not_prefix', 'not_contains'])
    expect(typeof operators[op]).toBe('string');
});

test('trend week interval is emitted with the fixed seven-day bucket size', () => {
  const input = form();
  input.mode = 'trend';
  input.interval = 'week';
  input.from = '2026-11-01T00:00';
  input.to = '2026-11-15T00:00';
  const request = buildRequest(input, 'development');
  expect(request.kind).toBe('trend');
  expect(request.interval).toBe('week');
  // A fourteen-day range stays inside the result-row limit at one bucket per week.
  expect(() => buildRequest(input, 'development')).not.toThrow();
});
