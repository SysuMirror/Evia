import { ApiError, type Environment, type RetentionPolicy } from '@evia/api-client';

export const RETENTION_MIN_DAYS = 30;
export const RETENTION_MAX_DAYS = 3650;

export type RetentionEnvironment = Environment;

export interface RetentionFormState {
  environment: RetentionEnvironment;
  retentionDays: string;
}

export function defaultRetentionForm(environment: RetentionEnvironment): RetentionFormState {
  return { environment, retentionDays: '' };
}

export const environmentLabels: Record<RetentionEnvironment, string> = {
  development: '开发环境',
  staging: '预发环境',
  production: '生产环境',
};

export const environmentOptions = (['development', 'staging', 'production'] as const).map(
  (value) => ({ value, label: environmentLabels[value] }),
);

/**
 * Validates the retention window before it leaves the browser. The server
 * remains the authority; these rules mirror RetentionPolicyInput and the
 * migration CHECK (30..3650 days) to reject obvious mistakes without a round trip.
 */
export function validateRetentionForm(form: RetentionFormState): string[] {
  const errors: string[] = [];
  const raw = form.retentionDays.trim();
  if (raw === '') {
    errors.push('请填写保留天数。');
    return errors;
  }
  if (!/^\d+$/.test(raw)) {
    errors.push('保留天数必须是正整数。');
    return errors;
  }
  const days = Number(raw);
  if (!Number.isSafeInteger(days) || days < RETENTION_MIN_DAYS || days > RETENTION_MAX_DAYS) {
    errors.push(`保留天数必须在 ${RETENTION_MIN_DAYS} 到 ${RETENTION_MAX_DAYS} 天之间。`);
  }
  return errors;
}

export function buildRetentionInput(form: RetentionFormState): {
  input: { environment: RetentionEnvironment; retention_days: number } | null;
  errors: string[];
} {
  const errors = validateRetentionForm(form);
  if (errors.length > 0) return { input: null, errors };
  return {
    input: { environment: form.environment, retention_days: Number(form.retentionDays.trim()) },
    errors: [],
  };
}

const revisionConflictCodes = new Set(['RETENTION_REVISION_CONFLICT', 'IDEMPOTENCY_CONFLICT']);

export function isRevisionConflict(error: unknown): boolean {
  return error instanceof ApiError && revisionConflictCodes.has(error.code);
}

export function isUncertainWrite(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}

/** Fills the editor from a fetched policy; an unconfigured scope stays blank. */
export function formFromPolicy(policy: RetentionPolicy | undefined): RetentionFormState {
  return {
    environment: (policy?.environment ?? 'development') as RetentionEnvironment,
    retentionDays: policy?.retention_days != null ? String(policy.retention_days) : '',
  };
}

export function formatPolicyInstant(value: string | null | undefined): string {
  if (!value) return '尚未配置';
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return value;
  return instant
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, ' UTC');
}

/** Human explanation of what the window means; enforcement is a separate worker. */
export function retentionSummary(days: number | null | undefined): string {
  if (days == null) return '当前未配置自动清理，事实将长期保留。';
  return `保留最近 ${days} 天的事实数据；超出窗口的历史数据由后续清理执行器按策略回收。`;
}
