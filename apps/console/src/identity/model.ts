import type { IdentityResolution, IdentityRevision } from '@evia/api-client';

export type MemberKind = 'uid' | 'anonymous_id';

export interface ResolveFormState {
  memberKind: MemberKind;
  memberValue: string;
}

export function defaultResolveForm(): ResolveFormState {
  return { memberKind: 'uid', memberValue: '' };
}

const UUID_PATTERN =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

export const IDENTITY_VALUE_MAX = 128;

/**
 * Validates the resolve lookup before it leaves the browser. The server
 * remains the authority; these rules mirror the resolve query contract and
 * reject obvious mistakes (notably a non-UUID anonymous identity) without a
 * round trip.
 */
export function validateResolveForm(form: ResolveFormState): string[] {
  const errors: string[] = [];
  const value = form.memberValue.trim();
  if (!value) {
    errors.push('请填写要解析的身份标识。');
  } else if (form.memberKind === 'anonymous_id' && !UUID_PATTERN.test(value)) {
    errors.push('匿名标识必须是 UUID 格式。');
  } else if (value.length > IDENTITY_VALUE_MAX) {
    errors.push(`身份标识不能超过 ${IDENTITY_VALUE_MAX} 个字符。`);
  }
  return errors;
}

type RevisionState = IdentityRevision['state'];

const STATE_LABELS: Record<RevisionState, string> = {
  building: '构建中',
  completed: '已完成',
  failed: '失败',
};

export function revisionStateLabel(state: RevisionState): string {
  return STATE_LABELS[state] ?? state;
}

type BadgeTone = 'neutral' | 'info' | 'warning' | 'success' | 'error';

const STATE_TONES: Record<RevisionState, BadgeTone> = {
  building: 'info',
  completed: 'success',
  failed: 'error',
};

export function revisionStateTone(state: RevisionState): BadgeTone {
  return STATE_TONES[state] ?? 'neutral';
}

/** Completed revisions that carry a member snapshot can be resolved directly. */
export function resolvableRevisions(revisions: IdentityRevision[]): IdentityRevision[] {
  return revisions.filter(
    (item) => item.state === 'completed' && item.snapshot_available !== false,
  );
}

/** The newest completed revision with a snapshot is the default resolve target. */
export function latestCompletedRevision(revisions: IdentityRevision[]): IdentityRevision | null {
  return resolvableRevisions(revisions)[0] ?? null;
}

export function resolveErrorMessage(
  status: number | undefined,
  code: string | undefined,
  fallback: string,
): string {
  if (code === 'IDENTITY_REVISION_PRUNED') {
    return '该身份修订已超出保留窗口并被自动清理，请选择仍在保留期内的修订。';
  }
  if (code === 'IDENTITY_REVISION_NOT_RESOLVABLE') {
    return '该修订尚未完成或缺少历史成员快照，无法解析。';
  }
  if (status === 404) {
    return '该身份尚未进入该修订的身份图。';
  }
  return fallback;
}

export function memberKindLabel(kind: string): string {
  if (kind === 'uid') return 'uid';
  if (kind === 'anonymous_id') return '匿名标识';
  return kind;
}

export function shortID(value: string): string {
  return value.length > 13 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value;
}

export function formatInstant(value: string | null | undefined): string {
  if (!value) return '未记录';
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return value;
  return instant
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, ' UTC');
}

export interface ResolutionSummary {
  person: string;
  members: { kind: string; value: string; firstSeen: string; joined: number }[];
}

export function resolutionSummary(resolution: IdentityResolution): ResolutionSummary {
  return {
    person: resolution.resolved_person_id,
    members: resolution.members.map((member) => ({
      kind: member.member_kind,
      value: member.member_value,
      firstSeen: formatInstant(member.first_seen_at),
      joined: member.joined_revision,
    })),
  };
}
