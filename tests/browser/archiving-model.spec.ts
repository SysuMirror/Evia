import { expect, test } from '@playwright/test';
import {
  backfillStatusLabel,
  backfillStatusTone,
  defaultBackfillForm,
  formatByteSize,
  formatInstant,
  isActiveBackfill,
  isBackfillable,
  manifestStatusLabel,
  manifestStatusTone,
  shortHash,
  validateBackfillForm,
} from '../../apps/console/src/archiving/model';
import type { ArchiveManifest, BackfillJob } from '../../packages/api-client/src/index';

const uuid = '33333333-3333-4333-8333-333333333333';

function manifestFixture(overrides: Partial<ArchiveManifest> = {}): ArchiveManifest {
  return {
    manifest_id: uuid,
    project_id: '11111111-1111-4111-8111-111111111111',
    environment: 'development',
    retention_days: 90,
    cutoff_at: '2026-08-13T10:00:00.000Z',
    window_start_at: '2026-05-01T00:00:00.000Z',
    window_end_at: '2026-08-12T00:00:00.000Z',
    row_count: 128,
    artifact_path: '/var/lib/evia/archives/x.jsonl',
    artifact_sha256: 'a'.repeat(64),
    artifact_bytes: 2048,
    status: 'materialized',
    error_code: null,
    sweep_id: null,
    created_at: '2026-08-13T10:05:00.000Z',
    completed_at: '2026-08-13T10:05:00.000Z',
    ...overrides,
  };
}

test('manifest status vocabulary maps every lifecycle state', () => {
  expect(manifestStatusLabel('pending')).toBe('物化中');
  expect(manifestStatusLabel('materialized')).toBe('已物化');
  expect(manifestStatusLabel('failed')).toBe('失败');
  expect(manifestStatusTone('materialized')).toBe('success');
  expect(manifestStatusTone('failed')).toBe('error');
});

test('backfill status vocabulary and active detection', () => {
  expect(backfillStatusLabel('queued')).toBe('排队中');
  expect(backfillStatusTone('completed')).toBe('success');
  const active: BackfillJob = {
    job_id: uuid,
    project_id: '11111111-1111-4111-8111-111111111111',
    environment: 'development',
    manifest_id: uuid,
    name: 'restore',
    state: 'running',
    rows_expected: 1,
    rows_inserted: 0,
    error_code: null,
    created_at: '2026-11-12T10:00:00.000Z',
    updated_at: '2026-11-12T10:00:00.000Z',
    finished_at: null,
  };
  expect(isActiveBackfill(active)).toBe(true);
  expect(isActiveBackfill({ ...active, state: 'completed' })).toBe(false);
  expect(isActiveBackfill({ ...active, state: 'cancelled' })).toBe(false);
});

test('only materialized manifests with artifacts are backfillable', () => {
  expect(isBackfillable(manifestFixture())).toBe(true);
  expect(isBackfillable(manifestFixture({ status: 'pending', artifact_path: null }))).toBe(false);
  expect(isBackfillable(manifestFixture({ status: 'failed', artifact_path: null }))).toBe(false);
  expect(isBackfillable(manifestFixture({ artifact_path: null }))).toBe(false);
});

test('backfill form validation guards name and manifest selection', () => {
  expect(validateBackfillForm(defaultBackfillForm()).length).toBeGreaterThan(0);
  const valid = { name: 'restore november', manifestId: uuid };
  expect(validateBackfillForm(valid)).toEqual([]);
  expect(validateBackfillForm({ name: '  ', manifestId: uuid })).not.toEqual([]);
  expect(validateBackfillForm({ name: 'x'.repeat(201), manifestId: uuid })).not.toEqual([]);
  expect(validateBackfillForm({ name: 'ok', manifestId: 'not-a-uuid' })).not.toEqual([]);
});

test('formatters degrade gracefully on missing or bad values', () => {
  expect(formatInstant(null)).toBe('—');
  expect(formatInstant('garbage')).toBe('garbage');
  expect(formatByteSize(undefined)).toBe('—');
  expect(formatByteSize(512)).toBe('512 B');
  expect(shortHash(null)).toBe('—');
  expect(shortHash('a'.repeat(64))).toContain('…');
  expect(shortHash('short')).toBe('short');
});
