import { expect, test } from '@playwright/test';
import type { IdentityResolution, IdentityRevision } from '../../packages/api-client/src/index';
import {
  defaultResolveForm,
  formatInstant,
  latestCompletedRevision,
  memberKindLabel,
  resolutionSummary,
  revisionStateLabel,
  revisionStateTone,
  shortID,
  validateResolveForm,
  type ResolveFormState,
} from '../../apps/console/src/identity/model';

function form(overrides: Partial<ResolveFormState> = {}): ResolveFormState {
  return { ...defaultResolveForm(), memberValue: 'user-123', ...overrides };
}

test('a uid lookup passes validation and an anonymous lookup requires a UUID', () => {
  expect(validateResolveForm(form())).toEqual([]);
  expect(
    validateResolveForm(form({ memberKind: 'anonymous_id', memberValue: 'not-a-uuid' })).length,
  ).toBeGreaterThan(0);
  expect(
    validateResolveForm(
      form({ memberKind: 'anonymous_id', memberValue: '11111111-1111-4111-8111-111111111111' }),
    ),
  ).toEqual([]);
});

test('missing or oversized values are rejected with user-facing errors', () => {
  expect(validateResolveForm(form({ memberValue: '  ' })).length).toBeGreaterThan(0);
  expect(validateResolveForm(form({ memberValue: 'x'.repeat(129) })).length).toBeGreaterThan(0);
});

test('revision state vocabulary maps to labels and tones', () => {
  expect(revisionStateLabel('completed')).toBe('已完成');
  expect(revisionStateLabel('failed')).toBe('失败');
  expect(revisionStateTone('completed')).toBe('success');
  expect(revisionStateTone('failed')).toBe('error');
  expect(revisionStateTone('building')).toBe('info');
});

function revision(overrides: Partial<IdentityRevision> = {}): IdentityRevision {
  return {
    revision: 2,
    edge_count: 4,
    person_count: 1,
    member_count: 4,
    state: 'completed',
    ...overrides,
  };
}

test('the latest completed revision is the resolvable target', () => {
  const items = [
    revision({ revision: 3, state: 'failed', error_code: 'IDENTITY_FOLD_COMMIT_FAILED' }),
    revision({ revision: 2 }),
    revision({ revision: 1 }),
  ];
  expect(latestCompletedRevision(items)?.revision).toBe(2);
  expect(
    latestCompletedRevision([revision({ revision: 3, state: 'failed', error_code: 'x' })]),
  ).toBeNull();
});

test('resolution summaries expose members with formatted instants', () => {
  const resolution: IdentityResolution = {
    revision: 2,
    resolved_person_id: '88888888-8888-4888-8888-888888888888',
    members: [
      {
        member_kind: 'uid',
        member_value: 'user-123',
        first_seen_at: '2026-11-20T04:00:00.500Z',
        joined_revision: 1,
      },
      {
        member_kind: 'anonymous_id',
        member_value: '11111111-1111-4111-8111-111111111111',
        first_seen_at: '2026-11-20T04:00:00Z',
        joined_revision: 2,
      },
    ],
  };
  const summary = resolutionSummary(resolution);
  expect(summary.person).toBe('88888888-8888-4888-8888-888888888888');
  expect(summary.members).toHaveLength(2);
  const first = summary.members[0]!;
  const second = summary.members[1]!;
  expect(first.kind).toBe('uid');
  expect(memberKindLabel('anonymous_id')).toBe('匿名标识');
  expect(first.firstSeen).toBe('2026-11-20 04:00:00 UTC');
  expect(second.firstSeen).toBe('2026-11-20 04:00:00 UTC');
});

test('formatting helpers degrade gracefully', () => {
  expect(formatInstant(undefined)).toBe('未记录');
  expect(formatInstant('not-a-date')).toBe('not-a-date');
  expect(shortID('88888888-8888-4888-8888-888888888888')).toBe('88888888…8888');
  expect(shortID('short')).toBe('short');
});
