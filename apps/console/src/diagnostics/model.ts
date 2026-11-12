import type { Diagnostics } from '@evia/api-client';
import type { Tone } from '@evia/design-system';

export type SectionStatus =
  | Diagnostics['sources']['status']
  | Diagnostics['budget']['status']
  | Diagnostics['pipeline']['status']
  | Diagnostics['rejections']['status'];

const statusLabels: Record<SectionStatus, string> = {
  ok: '正常',
  no_data: '暂无数据',
  unavailable: '不可用',
  disabled: '未启用',
};

const statusTones: Record<SectionStatus, Tone | 'neutral'> = {
  ok: 'success',
  no_data: 'warning',
  unavailable: 'error',
  disabled: 'neutral',
};

export function sectionStatusLabel(status: SectionStatus): string {
  return statusLabels[status] ?? status;
}

export function sectionStatusTone(status: SectionStatus): Tone | 'neutral' {
  return statusTones[status] ?? 'neutral';
}

export function featureLabel(enabled: boolean): string {
  return enabled ? '已启用' : '未启用';
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

/** Renders a bounded age without external date libraries. */
export function formatAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时`;
  return `${Math.floor(hours / 24)} 天`;
}

export function totalRejectionCount(report: Diagnostics): number {
  return report.rejections.totals.reduce((sum, item) => sum + item.count, 0);
}

export function shortUUID(value: string): string {
  return value.length <= 13 ? value : `${value.slice(0, 8)}…`;
}
