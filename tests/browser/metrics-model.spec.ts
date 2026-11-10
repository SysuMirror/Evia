import { expect, test } from '@playwright/test';
import {
  buildRunExecute,
  compatibleUnits,
  defaultFormState,
  defaultRunForm,
  describeChange,
  formatMetricInstant,
  fromDefinition,
  isNumericOp,
  supportsApproximate,
  toDefinition,
  type MetricFormState,
} from '../../apps/console/src/metrics/model';

function validState(overrides: Partial<MetricFormState> = {}): MetricFormState {
  return {
    ...defaultFormState(),
    title: '登录访客',
    events: [{ event_key: 'page_opened', schema_version: '1' }],
    ...overrides,
  };
}

test('aggregation compatibility drives units, approximate scope and numeric ops', () => {
  expect(compatibleUnits(validState({ op: 'count' }))).toEqual(['event']);
  expect(
    compatibleUnits(validState({ op: 'unique', subject: 'uid', fieldEnabled: false })),
  ).toEqual(['raw_uid']);
  expect(
    compatibleUnits(validState({ op: 'unique', subject: 'anonymous', fieldEnabled: false })),
  ).toEqual(['anonymous_id']);
  expect(
    compatibleUnits(validState({ op: 'unique', fieldEnabled: true, fieldKey: 'plan' })),
  ).toEqual(['distinct_value']);
  for (const op of ['sum', 'avg', 'min', 'max', 'p95'] as const)
    expect(compatibleUnits(validState({ op, fieldEnabled: true, fieldKey: 'duration' }))).toEqual([
      'number',
      'millisecond',
      'second',
      'byte',
    ]);
  expect(supportsApproximate('unique')).toBe(true);
  expect(supportsApproximate('p95')).toBe(true);
  expect(supportsApproximate('count')).toBe(false);
  expect(isNumericOp('sum')).toBe(true);
  expect(isNumericOp('count')).toBe(false);
});

test('definition conversion rejects malformed forms and accepts a complete one', () => {
  const blank = toDefinition(validState({ title: '   ' }));
  expect(blank.definition).toBeUndefined();
  expect(blank.errors.join(' ')).toContain('标题');

  const badEvent = toDefinition(
    validState({ events: [{ event_key: 'Bad-Name', schema_version: '0' }] }),
  );
  expect(badEvent.errors.some((error) => error.includes('事件'))).toBe(true);

  const numericWithoutField = toDefinition(
    validState({ op: 'sum', fieldEnabled: true, fieldKey: '', unit: 'number' }),
  );
  expect(numericWithoutField.errors.some((error) => error.includes('字段'))).toBe(true);

  const badJSON = toDefinition(validState({ filterJSON: '{not-json' }));
  expect(badJSON.errors.some((error) => error.includes('JSON'))).toBe(true);

  const ok = toDefinition(
    validState({
      op: 'unique',
      subject: 'uid',
      fieldEnabled: false,
      unit: 'raw_uid',
      description: '  去重 UID  ',
    }),
  );
  expect(ok.errors).toEqual([]);
  expect(ok.definition?.aggregation).toEqual({ op: 'unique', accuracy: 'exact' });
  expect(ok.definition?.description).toBe('去重 UID');
  expect(ok.definition?.filter).toBeUndefined();
});

test('form and wire definition round-trip without losing the fixed filter', () => {
  const state = validState({
    op: 'sum',
    fieldEnabled: true,
    fieldKey: 'duration_ms',
    unit: 'millisecond',
    filterJSON:
      '{"op":"eq","field":{"kind":"property","event_key":"page_opened","schema_version":1,"key":"page"},"value":"home"}',
  });
  const { definition, errors } = toDefinition(state);
  expect(errors).toEqual([]);
  const restored = fromDefinition(definition!);
  expect(restored.fieldKey).toBe('duration_ms');
  expect(restored.unit).toBe('millisecond');
  expect(JSON.parse(restored.filterJSON).op).toBe('eq');
  expect(toDefinition(restored).definition).toEqual(definition);
});

test('run execute enforces the half-open 31-day window, limit and filter JSON', () => {
  const form = defaultRunForm('UTC', new Date('2026-11-10T12:00:00Z'));
  expect(buildRunExecute(form).execute).toBeTruthy();

  const reversed = { ...form, from: form.to, to: form.from };
  expect(buildRunExecute(reversed).error).toContain('结束时间');

  const tooWide = {
    ...form,
    from: new Date(Date.parse(form.to) - 32 * 86400_000).toISOString().slice(0, 16),
  };
  expect(buildRunExecute(tooWide).error).toContain('31 天');

  expect(buildRunExecute({ ...form, limit: '0' }).error).toContain('行数');
  expect(buildRunExecute({ ...form, limit: '501' }).error).toContain('行数');
  expect(buildRunExecute({ ...form, filterJSON: '{' }).error).toContain('JSON');

  const withInterval = buildRunExecute({ ...form, interval: 'day' });
  expect(withInterval.execute?.interval).toBe('day');
});

test('run execute emits bounded group_by and rejects bad keys or too many fields', () => {
  const form = defaultRunForm('UTC', new Date('2026-11-10T12:00:00Z'));
  const withGroup = buildRunExecute({
    ...form,
    groupBy: [{ event_key: 'demo_page', schema_version: '1', key: 'page' }],
  });
  expect(withGroup.error).toBeUndefined();
  expect(withGroup.execute?.group_by).toEqual([
    { kind: 'property', event_key: 'demo_page', schema_version: 1, key: 'page' },
  ]);

  const badKey = buildRunExecute({
    ...form,
    groupBy: [{ event_key: 'demo_page', schema_version: '1', key: 'Bad-Key' }],
  });
  expect(badKey.error).toContain('snake_case');

  const tooMany = buildRunExecute({
    ...form,
    groupBy: [1, 2, 3, 4].map((index) => ({
      event_key: 'demo_page',
      schema_version: '1',
      key: `key_${index}`,
    })),
  });
  expect(tooMany.error).toContain('分组');

  const empty = buildRunExecute(form);
  expect(empty.execute?.group_by).toBeUndefined();
});

test('change labels and instant formatting stay bounded', () => {
  expect(
    describeChange({
      category: 'aggregation',
      path: '/aggregation/op',
      before: 'count',
      after: 'unique',
    }),
  ).toContain('聚合');
  expect(formatMetricInstant('2026-11-10T20:30:00Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  expect(formatMetricInstant('bad')).toBe('bad');
});
