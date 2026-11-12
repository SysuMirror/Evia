import { expect, test } from '@playwright/test';
import {
  featureLabel,
  formatAge,
  formatInstant,
  sectionStatusLabel,
  sectionStatusTone,
  shortUUID,
  totalRejectionCount,
} from '../../apps/console/src/diagnostics/model';
import type { Diagnostics } from '../../packages/api-client/src/index';

function diagnosticsFixture(overrides: Partial<Diagnostics> = {}): Diagnostics {
  return {
    project_id: '11111111-1111-4111-8111-111111111111',
    generated_at: '2026-11-12T10:00:00.000Z',
    features: {
      collection_enabled: true,
      analysis_enabled: false,
      delivery_enabled: true,
    },
    sources: {
      status: 'ok',
      total: 2,
      truncated: false,
      by_status: [{ name: 'active', count: 2 }],
      by_environment: [{ name: 'production', count: 2 }],
      disabled_sources: [],
    },
    budget: {
      status: 'disabled',
      rate_per_second: 0,
      burst: 0,
      environments: [],
    },
    pipeline: { status: 'no_data', proofs: [] },
    rejections: {
      status: 'ok',
      window_hours: 24,
      totals: [
        { reason: 'INVALID_EVENT', count: 3 },
        { reason: 'EVENT_TOO_LARGE', count: 2 },
      ],
      windows: [],
    },
    ...overrides,
  };
}

test('section status vocabulary covers every contract state', () => {
  expect(sectionStatusLabel('ok')).toBe('正常');
  expect(sectionStatusLabel('no_data')).toBe('暂无数据');
  expect(sectionStatusLabel('unavailable')).toBe('不可用');
  expect(sectionStatusLabel('disabled')).toBe('未启用');
  expect(sectionStatusTone('ok')).toBe('success');
  expect(sectionStatusTone('unavailable')).toBe('error');
  expect(sectionStatusTone('disabled')).toBe('neutral');
});

test('feature labels and instant formatting stay deterministic', () => {
  expect(featureLabel(true)).toBe('已启用');
  expect(featureLabel(false)).toBe('未启用');
  expect(formatInstant('2026-11-12T10:00:00.000Z')).toBe('2026-11-12 10:00:00 UTC');
  expect(formatInstant(null)).toBe('—');
  expect(formatInstant('not-a-date')).toBe('not-a-date');
});

test('age renderer uses bounded units', () => {
  expect(formatAge(0)).toBe('0 秒');
  expect(formatAge(45)).toBe('45 秒');
  expect(formatAge(120)).toBe('2 分钟');
  expect(formatAge(3600)).toBe('1 小时');
  expect(formatAge(90000)).toBe('1 天');
  expect(formatAge(-1)).toBe('—');
});

test('rejection totals aggregate every reason', () => {
  expect(totalRejectionCount(diagnosticsFixture())).toBe(5);
  const empty = diagnosticsFixture({
    rejections: { status: 'disabled', window_hours: 24, totals: [], windows: [] },
  });
  expect(totalRejectionCount(empty)).toBe(0);
});

test('short uuid keeps small values intact', () => {
  expect(shortUUID('abc')).toBe('abc');
  expect(shortUUID('12345678-1234-1234-1234-123456789abc')).toBe('12345678…');
});
