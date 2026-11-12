import { expect, test } from '@playwright/test';
import {
  defaultQuotaForm,
  QUOTA_BURST_MAX,
  QUOTA_BURST_MIN,
  QUOTA_RATE_MAX,
  QUOTA_RATE_MIN,
  quotaSourceLabel,
  runtimeStatusLabel,
  validateQuotaForm,
} from '../../apps/console/src/diagnostics/model';

test('quota form validation mirrors the Redis budget bounds', () => {
  expect(validateQuotaForm({ rate: '12.5', burst: '200' })).toEqual([]);
  expect(
    validateQuotaForm({ rate: String(QUOTA_RATE_MIN), burst: String(QUOTA_BURST_MIN) }),
  ).toEqual([]);

  const tooSmallRate = validateQuotaForm({ rate: '0', burst: '200' });
  expect(tooSmallRate.some((message) => message.includes('速率'))).toBe(true);

  const tooLargeBurst = validateQuotaForm({ rate: '10', burst: String(QUOTA_BURST_MAX + 1) });
  expect(tooLargeBurst.some((message) => message.includes('突发容量'))).toBe(true);

  expect(validateQuotaForm({ rate: 'not-a-number', burst: '200' })).toHaveLength(1);
  expect(validateQuotaForm({ rate: '10', burst: '49.5' })).toHaveLength(1);
  expect(validateQuotaForm(defaultQuotaForm())).toHaveLength(2);
  expect(QUOTA_RATE_MAX).toBe(1_000_000);
});

test('quota labels stay deterministic', () => {
  expect(quotaSourceLabel('default')).toBe('默认策略');
  expect(quotaSourceLabel('project_override')).toBe('项目覆盖');
  expect(runtimeStatusLabel('applied')).toBe('已生效');
  expect(runtimeStatusLabel('bucket_reset_pending')).toBe('桶重置待完成');
});
