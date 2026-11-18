import type { ExportInput, ExportJob, QueryRequest } from '@evia/api-client';

export type ExportKind = 'trend' | 'events';
export type ExportFormat = 'json' | 'csv' | 'ndjson';

export interface ExportFormState {
  name: string;
  environment: ExportInput['environment'];
  format: ExportFormat;
  kind: ExportKind;
  windowDays: number;
  limit: number;
  eventKeys: string;
}

export const EXPORT_NAME_MAX = 200;
export const EXPORT_LIMIT_MIN = 1;
export const EXPORT_LIMIT_MAX = 500;
const EVENT_KEY_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;
const MAX_EVENT_KEYS = 20;

/** Mirrors the server-side query budget: events span at most 7 days, trends 31. */
export function maxWindowDays(kind: ExportKind): number {
  return kind === 'events' ? 7 : 31;
}

export function defaultExportForm(environment: ExportInput['environment']): ExportFormState {
  return {
    name: '',
    environment,
    format: 'json',
    kind: 'trend',
    windowDays: 7,
    limit: 200,
    eventKeys: '',
  };
}

export function parseEventKeys(raw: string): string[] {
  return raw
    .split(',')
    .map((key) => key.trim())
    .filter((key) => key.length > 0);
}

export function validateExportForm(form: ExportFormState): string[] {
  const errors: string[] = [];
  const name = form.name.trim();
  if (!name) {
    errors.push('请填写导出名称。');
  } else if (name.length > EXPORT_NAME_MAX) {
    errors.push(`导出名称不能超过 ${EXPORT_NAME_MAX} 个字符。`);
  }
  const ceiling = maxWindowDays(form.kind);
  if (!Number.isInteger(form.windowDays) || form.windowDays < 1 || form.windowDays > ceiling) {
    errors.push(`时间窗口必须是 1 到 ${ceiling} 天之间的整数。`);
  }
  if (
    !Number.isInteger(form.limit) ||
    form.limit < EXPORT_LIMIT_MIN ||
    form.limit > EXPORT_LIMIT_MAX
  ) {
    errors.push(`结果行数上限必须在 ${EXPORT_LIMIT_MIN} 到 ${EXPORT_LIMIT_MAX} 之间。`);
  }
  const keys = parseEventKeys(form.eventKeys);
  if (keys.length > MAX_EVENT_KEYS) {
    errors.push(`事件标识最多 ${MAX_EVENT_KEYS} 个。`);
  } else if (keys.some((key) => !EVENT_KEY_PATTERN.test(key))) {
    errors.push('事件标识只能包含小写字母、数字和下划线，且以字母开头。');
  }
  return errors;
}

function isoUTC(instant: Date): string {
  return instant.toISOString().replace(/\.\d+Z$/, 'Z');
}

/**
 * Builds the bounded QueryRequest embedded in ExportInput. The server remains
 * the authority via analysis.Parse; this only guarantees well-formed,
 * budget-respecting requests from the console.
 */
export function buildQueryRequest(form: ExportFormState): QueryRequest {
  const to = new Date();
  const from = new Date(to.getTime() - form.windowDays * 24 * 60 * 60 * 1000);
  const keys = parseEventKeys(form.eventKeys);
  const request: QueryRequest = {
    kind: form.kind,
    environment: form.environment,
    time_range: { from: isoUTC(from), to: isoUTC(to) },
    timezone: 'UTC',
    limit: form.limit,
  };
  if (form.kind === 'events' && keys.length > 0) request.event_keys = keys;
  return request;
}

export function toExportInput(form: ExportFormState): {
  input: ExportInput | null;
  errors: string[];
} {
  const errors = validateExportForm(form);
  if (errors.length > 0) return { input: null, errors };
  // The generated inline ExportInput.query reference retains the JSON Schema
  // $defs key; the wire payload never carries it, so cross the generated type
  // boundary explicitly here instead of weakening the QueryRequest builder.
  const input = {
    name: form.name.trim(),
    environment: form.environment,
    format: form.format,
    query: buildQueryRequest(form),
  } as unknown as ExportInput;
  return { input, errors: [] };
}

type JobState = ExportJob['state'];

const STATE_LABELS: Record<JobState, string> = {
  queued: '排队中',
  running: '生成中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
};

export function exportStateLabel(state: JobState): string {
  return STATE_LABELS[state] ?? state;
}

type BadgeTone = 'neutral' | 'info' | 'warning' | 'success' | 'error';

const STATE_TONES: Record<JobState, BadgeTone> = {
  queued: 'neutral',
  running: 'info',
  completed: 'success',
  failed: 'error',
  cancelled: 'neutral',
};

export function exportStateTone(state: JobState): BadgeTone {
  return STATE_TONES[state] ?? 'neutral';
}

export function isExportTerminal(state: JobState): boolean {
  return state === 'completed' || state === 'failed' || state === 'cancelled';
}

/** Queued or running jobs keep the list polling until the worker settles them. */
export function hasActiveExportJob(jobs: ExportJob[]): boolean {
  return jobs.some((job) => !isExportTerminal(job.state));
}

export function formatInstant(value: string | null | undefined): string {
  if (!value) return '—';
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return value;
  return instant
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, ' UTC');
}

export function formatByteSize(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KiB', 'MiB', 'GiB'];
  let value = bytes;
  let unit = 'B';
  for (const candidate of units) {
    value /= 1024;
    unit = candidate;
    if (value < 1024) break;
  }
  return `${value.toFixed(1)} ${unit}`;
}
