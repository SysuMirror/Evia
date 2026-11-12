import { expect, test } from '@playwright/test';
import { artifactFileName, formatByteSize } from '../../apps/console/src/artifacts/model';

test('artifact file name is deterministic per release', () => {
  const id = '33333333-3333-4333-8333-333333333333';
  expect(artifactFileName(id)).toBe('events-33333333-3333-4333-8333-333333333333.ts');
});

test('byte size renderer uses bounded units', () => {
  expect(formatByteSize(0)).toBe('0 B');
  expect(formatByteSize(512)).toBe('512 B');
  expect(formatByteSize(2048)).toBe('2.0 KiB');
  expect(formatByteSize(5 * 1024 * 1024)).toBe('5.0 MiB');
});
