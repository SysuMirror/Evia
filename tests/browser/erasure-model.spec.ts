import { expect, test } from '@playwright/test';
import type { ErasureJob } from '../../packages/api-client/src/index';
import {
  defaultErasureForm,
  ERASURE_REASON_MAX,
  erasureStateLabel,
  erasureStateTone,
  formatBackupExpiry,
  hasActiveErasureJob,
  isErasureTerminal,
  toErasureInput,
  validateErasureForm,
  type ErasureFormState,
} from '../../apps/console/src/erasure/model';

function form(overrides: Partial<ErasureFormState> = {}): ErasureFormState {
  return {
    ...defaultErasureForm(),
    subjectValue: 'user-123',
    reason: '用户行使被遗忘权',
    ...overrides,
  };
}

test('a valid uid form builds the wire input with exactly one identity', () => {
  const built = toErasureInput(form());
  expect(built.errors).toEqual([]);
  expect(built.input).toEqual({
    environment: 'development',
    uid: 'user-123',
    reason: '用户行使被遗忘权',
  });
});

test('anonymous identifiers must be UUIDs while uids are free-form', () => {
  const badAnon = form({ subjectKind: 'anonymous_id', subjectValue: 'not-a-uuid' });
  expect(validateErasureForm(badAnon).length).toBeGreaterThan(0);
  expect(toErasureInput(badAnon).input).toBeNull();

  const goodAnon = form({
    subjectKind: 'anonymous_id',
    subjectValue: '11111111-1111-4111-8111-111111111111',
  });
  expect(toErasureInput(goodAnon).input).toEqual({
    environment: 'development',
    anonymous_id: '11111111-1111-4111-8111-111111111111',
    reason: '用户行使被遗忘权',
  });
});

test('missing identity or reason is rejected with user-facing errors', () => {
  expect(validateErasureForm(form({ subjectValue: '  ' })).length).toBeGreaterThan(0);
  expect(validateErasureForm(form({ reason: '' })).length).toBeGreaterThan(0);
  const longReason = 'x'.repeat(ERASURE_REASON_MAX + 1);
  expect(validateErasureForm(form({ reason: longReason })).length).toBeGreaterThan(0);
});

test('trims surrounding whitespace before building the request', () => {
  const built = toErasureInput(form({ subjectValue: '  user-123  ', reason: '  原因  ' }));
  expect(built.input?.uid).toBe('user-123');
  expect(built.input?.reason).toBe('原因');
});

test('the six job states map to deterministic labels and badge tones', () => {
  const states: ErasureJob['state'][] = [
    'queued',
    'hiding',
    'purging',
    'rebuilding',
    'completed',
    'failed',
  ];
  for (const state of states) {
    expect(erasureStateLabel(state).length).toBeGreaterThan(0);
    expect(['neutral', 'info', 'warning', 'success', 'error']).toContain(erasureStateTone(state));
  }
  expect(erasureStateTone('completed')).toBe('success');
  expect(erasureStateTone('failed')).toBe('error');
});

test('polling continues only while non-terminal jobs remain', () => {
  const job = (state: ErasureJob['state']): ErasureJob => ({
    job_id: '22222222-2222-4222-8222-222222222222',
    state,
  });
  expect(isErasureTerminal('completed')).toBe(true);
  expect(isErasureTerminal('failed')).toBe(true);
  expect(isErasureTerminal('purging')).toBe(false);
  expect(hasActiveErasureJob([job('completed'), job('failed')])).toBe(false);
  expect(hasActiveErasureJob([job('completed'), job('queued')])).toBe(true);
});

test('backup expiry renders an explicit placeholder instead of a fake date', () => {
  expect(formatBackupExpiry(null)).toBe('未记录');
  expect(formatBackupExpiry(undefined)).toBe('未记录');
  const rendered = formatBackupExpiry('2026-12-01T00:00:00Z');
  expect(rendered).toContain('2026-12-01');
  expect(rendered).toContain('UTC');
});
