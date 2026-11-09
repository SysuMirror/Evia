import { expect, test } from '@playwright/test';
import { ApiError, type ProjectMember } from '../../packages/api-client/src/index';
import {
  MAX_AUDIT_RANGE_DAYS,
  canAssignRole,
  defaultAuditRange,
  enabledOwnerCount,
  formatInstant,
  fromLocalInputs,
  isGovernanceRole,
  isLastEnabledOwner,
  isRevisionConflict,
  isUncertainWrite,
  roleLabel,
  roleOptions,
  shortID,
  toLocalInput,
  validateAuditRange,
} from '../../apps/console/src/governance/model';

function member(userID: string, role: ProjectMember['role'], disabled = false): ProjectMember {
  return {
    user_id: userID,
    username: userID,
    display_name: userID,
    role,
    disabled,
  };
}

test('governance roles match the authoritative contract labels', () => {
  expect(roleOptions.map((option) => option.value)).toEqual(['owner', 'editor', 'viewer']);
  expect(isGovernanceRole('editor')).toBe(true);
  expect(isGovernanceRole('developer')).toBe(false);
  expect(roleLabel('owner')).toBe('所有者');
});

test('the last enabled owner cannot be removed or demoted while other owners remain movable', () => {
  const onlyOwner = [member('a', 'owner'), member('b', 'editor'), member('c', 'viewer')];
  expect(enabledOwnerCount(onlyOwner)).toBe(1);
  expect(isLastEnabledOwner(onlyOwner, 'a')).toBe(true);
  expect(canAssignRole(onlyOwner, 'a', 'editor')).toBe(false);
  expect(canAssignRole(onlyOwner, 'a', 'viewer')).toBe(false);
  // Non-owners and disabled accounts are never protected by the last-owner rule.
  expect(isLastEnabledOwner(onlyOwner, 'b')).toBe(false);
  expect(canAssignRole(onlyOwner, 'b', 'viewer')).toBe(true);

  const twoOwners = [member('a', 'owner'), member('d', 'owner'), member('b', 'editor')];
  expect(isLastEnabledOwner(twoOwners, 'a')).toBe(false);
  expect(canAssignRole(twoOwners, 'a', 'editor')).toBe(true);

  // A disabled owner does not count toward the enabled owner guard.
  const disabledOwner = [member('a', 'owner'), member('d', 'owner', true)];
  expect(enabledOwnerCount(disabledOwner)).toBe(1);
  expect(isLastEnabledOwner(disabledOwner, 'd')).toBe(false);
  expect(isLastEnabledOwner(disabledOwner, 'a')).toBe(true);
});

test('revision conflicts and uncertain writes are classified distinctly', () => {
  expect(isRevisionConflict(new ApiError(409, 'MEMBERSHIP_REVISION_CONFLICT', '冲突'))).toBe(true);
  expect(isRevisionConflict(new ApiError(409, 'PROJECT_SETTINGS_REVISION_CONFLICT', '冲突'))).toBe(
    true,
  );
  expect(isRevisionConflict(new ApiError(404, 'NOT_FOUND', 'x'))).toBe(false);
  for (const status of [0, 500, 502, 503])
    expect(isUncertainWrite(new ApiError(status, 'X', 'x'))).toBe(true);
  for (const status of [400, 401, 403, 409, 422])
    expect(isUncertainWrite(new ApiError(status, 'X', 'x'))).toBe(false);
});

test('audit windows are half-open and capped at 31 days', () => {
  const from = '2026-11-01T00:00:00Z';
  expect(validateAuditRange({ from, to: '2026-11-01T00:00:00Z' })).toBeTruthy();
  expect(validateAuditRange({ from, to: '2026-10-31T00:00:00Z' })).toBeTruthy();
  expect(validateAuditRange({ from, to: '2026-11-02T00:00:00Z' })).toBeUndefined();
  expect(
    validateAuditRange({
      from,
      to: new Date(Date.parse(from) + (MAX_AUDIT_RANGE_DAYS + 1) * 86400_000).toISOString(),
    }),
  ).toContain(String(MAX_AUDIT_RANGE_DAYS));
});

test('default audit range covers the last seven days and local inputs round-trip', () => {
  const now = new Date('2026-11-10T12:00:00Z');
  const range = defaultAuditRange(now);
  expect(range.to).toBe(now.toISOString());
  expect(Date.parse(range.to) - Date.parse(range.from)).toBe(7 * 86400_000);
  const local = toLocalInput('2026-11-10T20:30:00+08:00');
  expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
  // Equal bounds parse but fail the half-open validation.
  const sameWindow = fromLocalInputs(local, local)!;
  expect(validateAuditRange(sameWindow)).toBeTruthy();
  const window = fromLocalInputs(local, toLocalInput('2026-11-11T20:30:00+08:00'))!;
  expect(validateAuditRange(window)).toBeUndefined();
});

test('instant and identifier formatters stay bounded', () => {
  expect(formatInstant('2026-11-10T20:30:45Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
  expect(formatInstant('not-a-date')).toBe('not-a-date');
  expect(shortID('short')).toBe('short');
  expect(shortID('abcdef12-3456-7890-abcd-ef1234567890')).toMatch(/…$/);
});
