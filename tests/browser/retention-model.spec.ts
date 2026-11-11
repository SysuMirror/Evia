import { expect, test } from '@playwright/test';
import { ApiError, type RetentionPolicy } from '../../packages/api-client/src/index';
import {
  buildRetentionInput,
  defaultRetentionForm,
  environmentLabels,
  environmentOptions,
  formFromPolicy,
  formatPolicyInstant,
  isRevisionConflict,
  isUncertainWrite,
  retentionSummary,
  RETENTION_MAX_DAYS,
  RETENTION_MIN_DAYS,
  validateRetentionForm,
  type RetentionFormState,
} from '../../apps/console/src/retention/model';

function form(overrides: Partial<RetentionFormState> = {}): RetentionFormState {
  return { ...defaultRetentionForm('production'), retentionDays: '365', ...overrides };
}

test('a valid form builds the wire retention input', () => {
  const built = buildRetentionInput(form());
  expect(built.errors).toEqual([]);
  expect(built.input).toEqual({ environment: 'production', retention_days: 365 });
});

test('retention days are bounded integers matching the migration check', () => {
  expect(RETENTION_MIN_DAYS).toBe(30);
  expect(RETENTION_MAX_DAYS).toBe(3650);
  expect(validateRetentionForm(form({ retentionDays: '29' })).length).toBeGreaterThan(0);
  expect(validateRetentionForm(form({ retentionDays: '3651' })).length).toBeGreaterThan(0);
  expect(validateRetentionForm(form({ retentionDays: '90.5' })).length).toBeGreaterThan(0);
  expect(validateRetentionForm(form({ retentionDays: 'abc' })).length).toBeGreaterThan(0);
  expect(validateRetentionForm(form({ retentionDays: '  ' })).length).toBeGreaterThan(0);
  expect(validateRetentionForm(form({ retentionDays: '30' }))).toEqual([]);
  expect(validateRetentionForm(form({ retentionDays: '3650' }))).toEqual([]);
  expect(buildRetentionInput(form({ retentionDays: '29' })).input).toBeNull();
});

test('environment options cover the three supported scopes with labels', () => {
  expect(environmentOptions.map((option) => option.value)).toEqual([
    'development',
    'staging',
    'production',
  ]);
  expect(environmentLabels.staging).toBe('预发环境');
});

test('an unconfigured policy yields a blank editor while a configured one fills it', () => {
  expect(formFromPolicy(undefined).retentionDays).toBe('');
  const policy: RetentionPolicy = {
    project_id: 'p',
    environment: 'staging',
    retention_days: 180,
    revision: 2,
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-10T00:00:00Z',
  };
  const filled = formFromPolicy(policy);
  expect(filled.environment).toBe('staging');
  expect(filled.retentionDays).toBe('180');
});

test('revision and uncertain-write failures are classified for safe retries', () => {
  expect(isRevisionConflict(new ApiError(409, 'RETENTION_REVISION_CONFLICT', '冲突'))).toBe(true);
  expect(isRevisionConflict(new ApiError(409, 'IDEMPOTENCY_CONFLICT', '冲突'))).toBe(true);
  expect(isRevisionConflict(new ApiError(400, 'INVALID_RETENTION_INPUT', '非法'))).toBe(false);
  expect(isUncertainWrite(new ApiError(0, 'NETWORK_ERROR', '网络'))).toBe(true);
  expect(isUncertainWrite(new ApiError(503, 'UNAVAILABLE', '不可用'))).toBe(true);
  expect(isUncertainWrite(new ApiError(409, 'RETENTION_REVISION_CONFLICT', '冲突'))).toBe(false);
});

test('timestamps and summaries never fabricate configuration', () => {
  expect(formatPolicyInstant(null)).toBe('尚未配置');
  expect(formatPolicyInstant(undefined)).toBe('尚未配置');
  expect(formatPolicyInstant('2026-12-01T00:00:00Z')).toContain('2026-12-01');
  expect(retentionSummary(null)).toContain('未配置');
  expect(retentionSummary(90)).toContain('90');
});
