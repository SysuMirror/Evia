import { expect, test } from '@playwright/test';
import {
  buildQueryRequest,
  defaultExportForm,
  exportSourceLabel,
  exportStateLabel,
  exportStateTone,
  formatByteSize,
  formatInstant,
  hasActiveExportJob,
  isExportTerminal,
  maxWindowDays,
  parseEventKeys,
  toExportInput,
  validateExportForm,
  type ExportFormState,
} from '../../apps/console/src/exports/model';
import {
  buildSavedExportInput,
  defaultSavedExportForm,
  validateSavedExportForm,
} from '../../apps/console/src/saved-analysis-run/export-model';
import type { SavedRunRange } from '../../apps/console/src/saved-analysis-run/types';
import type { SavedAnalysisVersion } from '../../packages/api-client/src/index';
import type { ExportJob } from '../../packages/api-client/src/index';

function form(overrides: Partial<ExportFormState> = {}): ExportFormState {
  return { ...defaultExportForm('development'), name: '每周导出', ...overrides };
}

test('a valid form builds the bounded export input', () => {
  const built = toExportInput(form());
  expect(built.errors).toEqual([]);
  expect(built.input?.name).toBe('每周导出');
  expect(built.input?.query.kind).toBe('trend');
  expect(built.input?.query.limit).toBe(200);
  expect(built.input?.query.timezone).toBe('UTC');
});

test('format defaults to json and is carried into the export input', () => {
  expect(defaultExportForm('development').format).toBe('json');
  expect(toExportInput(form()).input?.format).toBe('json');
  const csv = toExportInput(form({ format: 'csv' }));
  expect(csv.errors).toEqual([]);
  expect(csv.input?.format).toBe('csv');
  const ndjson = toExportInput(form({ format: 'ndjson' }));
  expect(ndjson.errors).toEqual([]);
  expect(ndjson.input?.format).toBe('ndjson');
  const parquet = toExportInput(form({ format: 'parquet' }));
  expect(parquet.errors).toEqual([]);
  expect(parquet.input?.format).toBe('parquet');
});

test('window ceiling follows the query kind budget', () => {
  expect(maxWindowDays('trend')).toBe(31);
  expect(maxWindowDays('events')).toBe(7);
  expect(validateExportForm(form({ kind: 'events', windowDays: 8 })).length).toBeGreaterThan(0);
  expect(validateExportForm(form({ kind: 'trend', windowDays: 31 })).length).toEqual(0);
  expect(validateExportForm(form({ windowDays: 0 })).length).toBeGreaterThan(0);
});

test('limit must stay inside the bounded 1-500 range', () => {
  expect(validateExportForm(form({ limit: 0 })).length).toBeGreaterThan(0);
  expect(validateExportForm(form({ limit: 501 })).length).toBeGreaterThan(0);
  expect(validateExportForm(form({ limit: 500 })).length).toEqual(0);
});

test('name is required and length bounded', () => {
  expect(validateExportForm(form({ name: '  ' })).length).toBeGreaterThan(0);
  expect(validateExportForm(form({ name: 'x'.repeat(201) })).length).toBeGreaterThan(0);
});

test('event keys are parsed and validated against the naming rule', () => {
  expect(parseEventKeys(' a, b ,,c ')).toEqual(['a', 'b', 'c']);
  expect(validateExportForm(form({ kind: 'events', eventKeys: 'Bad-Key' })).length).toBeGreaterThan(
    0,
  );
  const many = Array.from({ length: 21 }, (_, i) => `event_${i}`).join(',');
  expect(validateExportForm(form({ kind: 'events', eventKeys: many })).length).toBeGreaterThan(0);
  const built = toExportInput(form({ kind: 'events', eventKeys: 'lesson_opened, lesson_closed' }));
  expect(built.input?.query.event_keys).toEqual(['lesson_opened', 'lesson_closed']);
});

test('trend requests never carry event keys', () => {
  const built = toExportInput(form({ kind: 'trend', eventKeys: 'lesson_opened' }));
  expect(built.input?.query.event_keys).toBeUndefined();
});

test('built time range is ordered and ends near now', () => {
  const before = Date.now();
  const request = buildQueryRequest(form({ kind: 'trend', windowDays: 3 }));
  const after = Date.now();
  const from = new Date(request.time_range.from).getTime();
  const to = new Date(request.time_range.to).getTime();
  expect(to - from).toBeGreaterThan(2 * 24 * 3600 * 1000);
  // isoUTC truncates milliseconds, compare at second granularity.
  expect(to).toBeGreaterThanOrEqual(before - 1000);
  expect(to).toBeLessThanOrEqual(after + 1000);
});

test('state vocabulary maps every lifecycle state', () => {
  const states: ExportJob['state'][] = ['queued', 'running', 'completed', 'failed', 'cancelled'];
  for (const state of states) {
    expect(exportStateLabel(state)).not.toBe(state);
    expect(exportStateTone(state)).toBeTruthy();
  }
  expect(isExportTerminal('queued')).toBe(false);
  expect(isExportTerminal('completed')).toBe(true);
});

test('active job detection drives list polling', () => {
  const queued = { state: 'queued' } as ExportJob;
  const done = { state: 'completed' } as ExportJob;
  expect(hasActiveExportJob([queued, done])).toBe(true);
  expect(hasActiveExportJob([done])).toBe(false);
});

test('formatters degrade gracefully on missing or bad values', () => {
  expect(formatByteSize(undefined)).toBe('—');
  expect(formatByteSize(512)).toBe('512 B');
  expect(formatByteSize(2048)).toBe('2.0 KiB');
  expect(formatInstant(null)).toBe('—');
  expect(formatInstant('not-a-date')).toBe('not-a-date');
  expect(formatInstant('2026-11-12T10:00:00Z')).toContain('UTC');
});

test('export source label distinguishes version exports from inline jobs', () => {
  expect(exportSourceLabel({ source_analysis_version: null } as ExportJob)).toBe('内联查询');
  expect(exportSourceLabel({ source_analysis_version: 3 } as ExportJob)).toBe('保存分析 v3');
});

const runRange: SavedRunRange = {
  from: '2026-11-07T10:00',
  to: '2026-11-07T11:00',
  timezone: 'UTC',
};

test('saved version export builds the bounded input without an inline query', () => {
  const form = { name: '版本导出', format: 'csv' as const };
  const built = buildSavedExportInput(runRange, 'trend', form);
  expect(built.errors).toEqual([]);
  expect(built.input).toEqual({
    name: '版本导出',
    format: 'csv',
    time_range: { from: '2026-11-07T10:00:00.000Z', to: '2026-11-07T11:00:00.000Z' },
    timezone: 'UTC',
  });
  expect((built.input ?? {}).timezone).toBe('UTC');
});

test('saved version export validates name length', () => {
  expect(validateSavedExportForm({ name: '  ', format: 'json' }).length).toBeGreaterThan(0);
  expect(validateSavedExportForm({ name: 'x'.repeat(201), format: 'json' }).length).toBeGreaterThan(
    0,
  );
  expect(validateSavedExportForm({ name: '合规名称', format: 'ndjson' })).toEqual([]);
});

test('saved version export reuses the run range budget by query kind', () => {
  const overRange: SavedRunRange = {
    ...runRange,
    from: '2026-10-06T11:00',
    to: '2026-11-07T11:00',
  };
  expect(
    buildSavedExportInput(overRange, 'trend', { name: '超期', format: 'json' }).input,
  ).toBeNull();
  const funnelOver: SavedRunRange = {
    ...runRange,
    from: '2026-10-30T11:00',
    to: '2026-11-07T11:00',
  };
  expect(
    buildSavedExportInput(funnelOver, 'funnel', { name: '漏斗超期', format: 'json' }).input,
  ).toBeNull();
  const inverted: SavedRunRange = { ...runRange, from: runRange.to, to: runRange.from };
  expect(
    buildSavedExportInput(inverted, 'trend', { name: '反向', format: 'json' }).errors.length,
  ).toBeGreaterThan(0);
});

test('default saved export form derives its name from the immutable title', () => {
  const version = {
    analysis_id: '77777777-7777-4777-8777-777777777777',
    version: 2,
    definition: { title: '课程完课率' },
  } as SavedAnalysisVersion;
  const form = defaultSavedExportForm(version);
  expect(form.name).toBe('课程完课率 导出');
  expect(form.format).toBe('json');
});
