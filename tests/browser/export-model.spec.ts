import { expect, test } from '@playwright/test';
import {
  buildQueryRequest,
  defaultExportForm,
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
