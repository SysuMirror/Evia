import { expect, test } from '@playwright/test';
import {
  buildFormulaRun,
  defaultFormState,
  defaultRunForm,
  formatFormulaInstant,
  fromFormulaDefinition,
  maxFormulaMetricSearchRunes,
  normalizeFormulaMetricSearch,
  toFormulaDefinition,
  type FormulaFormState,
} from '../../apps/console/src/formula-metrics/model';

const metricA = '11111111-1111-4111-8111-111111111111';
const metricB = '22222222-2222-4222-8222-222222222222';

function validState(overrides: Partial<FormulaFormState> = {}): FormulaFormState {
  return {
    ...defaultFormState(),
    title: '转化率',
    left: { kind: 'metric', metricID: metricA, version: '1', constant: '' },
    right: { kind: 'metric', metricID: metricB, version: '1', constant: '' },
    ...overrides,
  };
}

test('builds a binary metric reference formula definition', () => {
  const { definition, errors } = toFormulaDefinition(validState());
  expect(errors).toEqual([]);
  expect(definition?.formula.expression.op).toBe('divide');
  expect(definition?.formula.expression.left).toEqual({
    op: 'metric',
    metric: { metric_id: metricA, version: 1 },
  });
  expect(definition?.formula.unit).toBe('number');
});

test('supports a constant operand and rejects unsafe constants', () => {
  const scaled = validState({
    operator: 'multiply',
    right: { kind: 'constant', metricID: '', version: '1', constant: '100' },
  });
  expect(toFormulaDefinition(scaled).definition?.formula.expression.right).toEqual({
    op: 'constant',
    value: 100,
  });
  const unsafe = validState({
    right: { kind: 'constant', metricID: '', version: '1', constant: '9007199254740992' },
  });
  expect(toFormulaDefinition(unsafe).errors.length).toBeGreaterThan(0);
});

test('rejects bad titles, malformed references and duplicate operands', () => {
  expect(toFormulaDefinition(validState({ title: '   ' })).errors.length).toBeGreaterThan(0);
  const badRef = validState({
    left: { kind: 'metric', metricID: 'not-a-uuid', version: '1', constant: '' },
  });
  expect(toFormulaDefinition(badRef).errors.length).toBeGreaterThan(0);
  const duplicate = validState({
    right: { kind: 'metric', metricID: metricA, version: '1', constant: '' },
  });
  expect(toFormulaDefinition(duplicate).errors.length).toBeGreaterThan(0);
});

test('round trips a stored binary definition into the builder form', () => {
  const { definition } = toFormulaDefinition(validState());
  expect(definition).toBeTruthy();
  const state = fromFormulaDefinition(definition!);
  expect(state.operator).toBe('divide');
  expect(state.left.metricID).toBe(metricA);
  expect(state.right.metricID).toBe(metricB);
});

test('run form enforces window and limit budgets', () => {
  const form = defaultRunForm('UTC');
  const ok = buildFormulaRun(form);
  expect(ok.error).toBeUndefined();
  expect(ok.execute?.timezone).toBe('UTC');
  const tooLong = buildFormulaRun({
    ...form,
    to: new Date(new Date(form.from).getTime() + 32 * 86400_000).toISOString().slice(0, 16),
  });
  expect(tooLong.error).toContain('31');
  const badLimit = buildFormulaRun({ ...form, limit: '501' });
  expect(badLimit.error).toContain('500');
});

test('formats instants without throwing on invalid input', () => {
  expect(formatFormulaInstant('2026-11-10T08:30:00Z')).toMatch(/2026-/);
  expect(formatFormulaInstant('not-a-date')).toBe('not-a-date');
});

test('formula metric search terms trim blanks and clamp to the server rune limit', () => {
  expect(normalizeFormulaMetricSearch('  留存 公式 ')).toBe('留存 公式');
  expect(normalizeFormulaMetricSearch('   ')).toBe('');
  expect(normalizeFormulaMetricSearch('')).toBe('');
  const atLimit = '字'.repeat(maxFormulaMetricSearchRunes);
  expect(normalizeFormulaMetricSearch(atLimit)).toBe(atLimit);
  const overLimit = '字'.repeat(maxFormulaMetricSearchRunes + 1);
  const clamped = normalizeFormulaMetricSearch(overLimit);
  expect([...clamped].length).toBe(maxFormulaMetricSearchRunes);
  expect(clamped).toBe(atLimit);
  const emojiClamped = normalizeFormulaMetricSearch(
    ` ${'😀'.repeat(maxFormulaMetricSearchRunes + 1)} `,
  );
  expect([...emojiClamped].length).toBe(maxFormulaMetricSearchRunes);
  expect(emojiClamped.startsWith('😀')).toBe(true);
  expect(normalizeFormulaMetricSearch('a\tb  c')).toBe('a\tb  c');
});
