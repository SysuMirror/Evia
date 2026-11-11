import type { ErasureInput, ErasureJob } from '@evia/api-client';

export type SubjectKind = 'uid' | 'anonymous_id';

export interface ErasureFormState {
  environment: ErasureInput['environment'];
  subjectKind: SubjectKind;
  subjectValue: string;
  reason: string;
}

export function defaultErasureForm(): ErasureFormState {
  return {
    environment: 'development',
    subjectKind: 'uid',
    subjectValue: '',
    reason: '',
  };
}

const UUID_PATTERN =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

export const ERASURE_REASON_MAX = 1024;
export const ERASURE_UID_MAX = 128;

/**
 * Validates the destructive request before it leaves the browser. The server
 * remains the authority; these rules mirror ErasureInput and reject obvious
 * mistakes (notably a non-UUID anonymous identity) without a round trip.
 */
export function validateErasureForm(form: ErasureFormState): string[] {
  const errors: string[] = [];
  const subject = form.subjectValue.trim();
  if (!subject) {
    errors.push('请填写要删除的身份标识。');
  } else if (form.subjectKind === 'anonymous_id' && !UUID_PATTERN.test(subject)) {
    errors.push('匿名标识必须是 UUID 格式。');
  } else if (subject.length > ERASURE_UID_MAX) {
    errors.push(`身份标识不能超过 ${ERASURE_UID_MAX} 个字符。`);
  }
  const reason = form.reason.trim();
  if (!reason) {
    errors.push('请填写删除原因，用于审计追溯。');
  } else if (reason.length > ERASURE_REASON_MAX) {
    errors.push(`删除原因不能超过 ${ERASURE_REASON_MAX} 个字符。`);
  }
  return errors;
}

export function toErasureInput(form: ErasureFormState): {
  input: ErasureInput | null;
  errors: string[];
} {
  const errors = validateErasureForm(form);
  if (errors.length > 0) return { input: null, errors };
  const base = {
    environment: form.environment,
    reason: form.reason.trim(),
  };
  const subject = form.subjectValue.trim();
  const input: ErasureInput =
    form.subjectKind === 'uid' ? { ...base, uid: subject } : { ...base, anonymous_id: subject };
  return { input, errors: [] };
}

type JobState = ErasureJob['state'];

const STATE_LABELS: Record<JobState, string> = {
  queued: '排队中',
  hiding: '建立删除屏障',
  purging: '删除事实数据',
  rebuilding: '校验零残留',
  completed: '已完成',
  failed: '失败',
};

export function erasureStateLabel(state: JobState): string {
  return STATE_LABELS[state] ?? state;
}

type BadgeTone = 'neutral' | 'info' | 'warning' | 'success' | 'error';

const STATE_TONES: Record<JobState, BadgeTone> = {
  queued: 'neutral',
  hiding: 'info',
  purging: 'warning',
  rebuilding: 'warning',
  completed: 'success',
  failed: 'error',
};

export function erasureStateTone(state: JobState): BadgeTone {
  return STATE_TONES[state] ?? 'neutral';
}

export function isErasureTerminal(state: JobState): boolean {
  return state === 'completed' || state === 'failed';
}

/** Jobs still driving the worker keep the list polling until they settle. */
export function hasActiveErasureJob(jobs: ErasureJob[]): boolean {
  return jobs.some((job) => !isErasureTerminal(job.state));
}

export function formatBackupExpiry(value: string | null | undefined): string {
  if (!value) return '未记录';
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return value;
  return instant
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, ' UTC');
}
