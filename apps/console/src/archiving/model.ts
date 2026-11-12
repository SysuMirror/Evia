import type { ArchiveManifest, BackfillJob, Environment } from '@evia/api-client';

export type ArchiveEnvironment = Environment;

export const environmentLabels: Record<ArchiveEnvironment, string> = {
  development: '开发环境',
  staging: '预发环境',
  production: '生产环境',
};

export const environmentOptions = (['development', 'staging', 'production'] as const).map(
  (value) => ({ value, label: environmentLabels[value] }),
);

export type Tone = 'neutral' | 'success' | 'error' | 'info' | 'warning';

const manifestStatusLabels: Record<ArchiveManifest['status'], string> = {
  pending: '物化中',
  materialized: '已物化',
  failed: '失败',
};

const manifestStatusTones: Record<ArchiveManifest['status'], Tone> = {
  pending: 'info',
  materialized: 'success',
  failed: 'error',
};

export function manifestStatusLabel(status: ArchiveManifest['status']): string {
  return manifestStatusLabels[status] ?? status;
}

export function manifestStatusTone(status: ArchiveManifest['status']): Tone {
  return manifestStatusTones[status] ?? 'neutral';
}

const backfillStatusLabels: Record<BackfillJob['state'], string> = {
  queued: '排队中',
  running: '生成中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
};

const backfillStatusTones: Record<BackfillJob['state'], Tone> = {
  queued: 'neutral',
  running: 'info',
  completed: 'success',
  failed: 'error',
  cancelled: 'warning',
};

export function backfillStatusLabel(state: BackfillJob['state']): string {
  return backfillStatusLabels[state] ?? state;
}

export function backfillStatusTone(state: BackfillJob['state']): Tone {
  return backfillStatusTones[state] ?? 'neutral';
}

export function isActiveBackfill(job: BackfillJob): boolean {
  return job.state === 'queued' || job.state === 'running';
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
  if (bytes == null) return '—';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

export function shortHash(value: string | null | undefined): string {
  if (!value) return '—';
  return value.length <= 16 ? value : `${value.slice(0, 12)}…`;
}

export const BACKFILL_NAME_MAX = 200;

export interface BackfillFormState {
  name: string;
  manifestId: string;
}

export function defaultBackfillForm(): BackfillFormState {
  return { name: '', manifestId: '' };
}

export function validateBackfillForm(form: BackfillFormState): string[] {
  const errors: string[] = [];
  const name = form.name.trim();
  if (name.length < 1 || name.length > BACKFILL_NAME_MAX) {
    errors.push(`回补名称长度必须在 1 到 ${BACKFILL_NAME_MAX} 字符之间。`);
  }
  if (
    !/^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(
      form.manifestId,
    )
  ) {
    errors.push('请选择一个已物化的归档清单。');
  }
  return errors;
}

/** A manifest can only backfill once it has a durable, checksummed artifact. */
export function isBackfillable(manifest: ArchiveManifest): boolean {
  return manifest.status === 'materialized' && manifest.artifact_path != null;
}
