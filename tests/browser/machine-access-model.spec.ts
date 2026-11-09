import { readFile } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import { ApiError, type MachineCredential } from '../../packages/api-client/src/index';
import {
  credentialState,
  permissionGroups,
  uncertain,
  validateExpiry,
} from '../../apps/console/src/machine-access/model';

test('machine grant choices cover the authoritative contract without duplicate grants', async () => {
  const contract = JSON.parse(await readFile('contracts/http/openapi.json', 'utf8'));
  const choices = permissionGroups.flatMap((group) => group.items.map((item) => item.value));
  expect(new Set(choices).size).toBe(choices.length);
  expect([...choices].sort()).toEqual(
    [...contract.components.schemas.MachinePermission.enum].sort(),
  );
});

test('machine expiry checks use inclusive TTL boundaries and revocation wins over expiry', () => {
  const now = Date.parse('2026-11-09T12:00:00Z');
  expect(validateExpiry('invalid', now)).toBeTruthy();
  expect(validateExpiry(new Date(now + 59_999).toISOString(), now)).toBeTruthy();
  expect(validateExpiry(new Date(now + 60_000).toISOString(), now)).toBeUndefined();
  expect(validateExpiry(new Date(now + 90 * 86400_000).toISOString(), now)).toBeUndefined();
  expect(validateExpiry(new Date(now + 90 * 86400_000 + 1).toISOString(), now)).toBeTruthy();
  const credential: MachineCredential = {
    id: 'credential',
    principal_id: 'machine',
    created_at: new Date(now - 1000).toISOString(),
    expires_at: new Date(now).toISOString(),
    revoked_at: null,
  };
  expect(credentialState(credential, now - 1).label).toBe('有效');
  expect(credentialState(credential, now).label).toBe('已过期');
  expect(credentialState({ ...credential, revoked_at: credential.created_at }, now - 1).label).toBe(
    '已撤销',
  );
});

test('machine retry recovery preserves ambiguous outcomes while definite client errors remain editable', () => {
  expect(uncertain(undefined)).toBe(false);
  expect(uncertain(new Error('lost response'))).toBe(true);
  for (const status of [0, 500, 502, 503])
    expect(uncertain(new ApiError(status, 'FAILURE', 'failed'))).toBe(true);
  for (const status of [400, 401, 403, 404, 409, 422, 429])
    expect(uncertain(new ApiError(status, 'FAILURE', 'failed'))).toBe(false);
});
