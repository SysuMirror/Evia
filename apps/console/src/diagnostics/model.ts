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

export const QUOTA_RATE_MIN = 0.001;
export const QUOTA_RATE_MAX = 1_000_000;
export const QUOTA_BURST_MIN = 50;
export const QUOTA_BURST_MAX = 1_000_000;

export interface QuotaFormState {
  rate: string;
  burst: string;
}

export function defaultQuotaForm(): QuotaFormState {
  return { rate: '', burst: '' };
}

export function validateQuotaForm(form: QuotaFormState): string[] {
  const errors: string[] = [];
  const rate = Number(form.rate);
  if (
    form.rate.trim() === '' ||
    !Number.isFinite(rate) ||
    rate < QUOTA_RATE_MIN ||
    rate > QUOTA_RATE_MAX
  ) {
    errors.push(`速率必须是 ${QUOTA_RATE_MIN} 到 ${QUOTA_RATE_MAX} 事件/秒之间的有限数。`);
  }
  const burst = Number(form.burst);
  if (!/^\d+$/.test(form.burst.trim()) || burst < QUOTA_BURST_MIN || burst > QUOTA_BURST_MAX) {
    errors.push(`突发容量必须是 ${QUOTA_BURST_MIN} 到 ${QUOTA_BURST_MAX} 之间的整数。`);
  }
  return errors;
}

export function quotaSourceLabel(source: 'default' | 'project_override'): string {
  return source === 'project_override' ? '项目覆盖' : '默认策略';
}

export function runtimeStatusLabel(status: 'applied' | 'bucket_reset_pending'): string {
  return status === 'applied' ? '已生效' : '桶重置待完成';
}
