import { ApiError, type ProjectMember } from '@evia/api-client';

export const governanceRoles = ['owner', 'editor', 'viewer'] as const;
export type GovernanceRole = (typeof governanceRoles)[number];

export const roleLabels: Record<GovernanceRole, string> = {
  owner: '所有者',
  editor: '编辑者',
  viewer: '只读成员',
};

export const roleOptions: { value: GovernanceRole; label: string }[] = governanceRoles.map(
  (role) => ({ value: role, label: roleLabels[role] }),
);

export const actorKindLabels: Record<'human' | 'machine', string> = {
  human: '成员',
  machine: '机器',
};

export function isGovernanceRole(value: string): value is GovernanceRole {
  return (governanceRoles as readonly string[]).includes(value);
}

export function roleLabel(role: string): string {
  return isGovernanceRole(role) ? roleLabels[role] : role;
}

/** Enabled owners are the only accounts that keep owning the project. */
export function enabledOwnerCount(members: readonly ProjectMember[]): number {
  return members.filter((member) => member.role === 'owner' && !member.disabled).length;
}

/**
 * The last enabled owner can neither be removed nor moved to a non-owner role.
 * Disabled members and non-owner members can always be targeted by an owner.
 */
export function isLastEnabledOwner(members: readonly ProjectMember[], userID: string): boolean {
  const target = members.find((member) => member.user_id === userID);
  if (!target || target.disabled || target.role !== 'owner') return false;
  return enabledOwnerCount(members) <= 1;
}

export function canAssignRole(
  members: readonly ProjectMember[],
  userID: string,
  nextRole: string,
): boolean {
  if (!isGovernanceRole(nextRole)) return false;
  if (nextRole !== 'owner' && isLastEnabledOwner(members, userID)) return false;
  return true;
}

const revisionConflictCodes = new Set([
  'MEMBERSHIP_REVISION_CONFLICT',
  'PROJECT_SETTINGS_REVISION_CONFLICT',
]);

export function isRevisionConflict(error: unknown): boolean {
  return error instanceof ApiError && revisionConflictCodes.has(error.code);
}

export function isUncertainWrite(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}

export const MAX_AUDIT_RANGE_DAYS = 31;

export interface AuditRangeInput {
  from: string;
  to: string;
}

/** Build the default half-open audit window: the last seven days up to now. */
export function defaultAuditRange(now: Date = new Date()): AuditRangeInput {
  const to = now.getTime();
  const from = to - 7 * 24 * 60 * 60 * 1000;
  return { from: new Date(from).toISOString(), to: new Date(to).toISOString() };
}

function toRFC3339(localValue: string): string | undefined {
  if (!localValue) return undefined;
  const parsed = new Date(localValue);
  if (Number.isNaN(parsed.getTime())) return undefined;
  return parsed.toISOString();
}

/** Value for <input type="datetime-local"> from an RFC3339 instant. */
export function toLocalInput(instant: string): string {
  const parsed = new Date(instant);
  if (Number.isNaN(parsed.getTime())) return '';
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}T${pad(
    parsed.getHours(),
  )}:${pad(parsed.getMinutes())}`;
}

export function fromLocalInputs(from: string, to: string): AuditRangeInput | undefined {
  const fromISO = toRFC3339(from);
  const toISO = toRFC3339(to);
  if (!fromISO || !toISO) return undefined;
  return { from: fromISO, to: toISO };
}

/** Validate the half-open [from, to) window, capped at 31 days. */
export function validateAuditRange(range: AuditRangeInput): string | undefined {
  const from = new Date(range.from);
  const to = new Date(range.to);
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime())) return '请提供有效的起止时间。';
  if (!(to.getTime() > from.getTime())) return '结束时间必须晚于开始时间。';
  if (to.getTime() - from.getTime() > MAX_AUDIT_RANGE_DAYS * 24 * 60 * 60 * 1000)
    return `审计查询范围最长 ${MAX_AUDIT_RANGE_DAYS} 天。`;
  return undefined;
}

/** Format an RFC3339 instant in the project operator's local wall clock. */
export function formatInstant(instant: string): string {
  const parsed = new Date(instant);
  if (Number.isNaN(parsed.getTime())) return instant;
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(
    parsed.getHours(),
  )}:${pad(parsed.getMinutes())}:${pad(parsed.getSeconds())}`;
}

export function shortID(identifier: string): string {
  return identifier.length <= 12 ? identifier : `${identifier.slice(0, 8)}…`;
}
