import {
  ApiError,
  type Environment,
  type MachineCredential,
  type MachinePermission,
} from '@evia/api-client';

export const environmentLabels: Record<Environment, string> = {
  development: '开发环境',
  staging: '预发布环境',
  production: '生产环境',
};
export const permissionGroups: {
  name: string;
  help?: string;
  items: { value: MachinePermission; label: string }[];
}[] = [
  { name: '项目', items: [{ value: 'project.read', label: '读取项目' }] },
  {
    name: '事件与发布',
    help: '事件草稿属于整个项目，草稿权限可读取或修改跨环境共用的定义。',
    items: [
      { value: 'catalog.drafts.read', label: '读取事件草稿' },
      { value: 'catalog.drafts.write', label: '编辑事件草稿' },
      { value: 'catalog.releases.read', label: '读取发布' },
      { value: 'catalog.releases.publish', label: '创建发布' },
      { value: 'catalog.releases.activate', label: '激活发布' },
    ],
  },
  {
    name: '接入来源',
    items: [
      { value: 'sources.read', label: '读取来源' },
      { value: 'sources.write', label: '管理来源' },
      { value: 'sources.rotate', label: '轮换来源密钥' },
    ],
  },
  {
    name: '查询',
    help: '原始行为数据权限允许读取标识和事件明细，请按需授权。',
    items: [
      { value: 'queries.read', label: '读取查询' },
      { value: 'queries.run', label: '执行查询' },
      { value: 'queries.raw.read', label: '读取原始行为数据' },
    ],
  },
  {
    name: '保存的分析',
    items: [
      { value: 'analyses.read', label: '读取分析' },
      { value: 'analyses.write', label: '编辑分析' },
      { value: 'analyses.publish', label: '发布分析' },
      { value: 'analyses.run', label: '运行分析' },
    ],
  },
  {
    name: '看板',
    items: [
      { value: 'dashboards.read', label: '读取看板' },
      { value: 'dashboards.write', label: '编辑看板' },
      { value: 'dashboards.publish', label: '发布看板' },
      { value: 'dashboards.run', label: '运行看板' },
    ],
  },
];
export function uncertain(error: Error | undefined) {
  return !!error && (!(error instanceof ApiError) || error.status === 0 || error.status >= 500);
}
export function dateLabel(value: string | null) {
  if (!value) return '—';
  return new Date(value).toLocaleString('zh-CN', { hour12: false });
}
export function credentialState(credential: MachineCredential, now = Date.now()) {
  if (credential.revoked_at) return { label: '已撤销', tone: 'neutral' as const };
  if (Date.parse(credential.expires_at) <= now)
    return { label: '已过期', tone: 'neutral' as const };
  return { label: '有效', tone: 'success' as const };
}
export function localExpiry(days = 30) {
  const value = new Date(Date.now() + days * 86400_000);
  value.setMinutes(value.getMinutes() - value.getTimezoneOffset());
  return value.toISOString().slice(0, 16);
}
export function validateExpiry(value: string, now = Date.now()): string | undefined {
  const timestamp = new Date(value).getTime();
  if (!Number.isFinite(timestamp)) return '请选择有效的到期时间';
  const remaining = timestamp - now;
  if (remaining < 60_000) return '到期时间至少为当前时间之后 1 分钟';
  if (remaining > 90 * 86400_000) return '凭据有效期不能超过 90 天';
}
