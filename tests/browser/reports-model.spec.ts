import { expect, test } from '@playwright/test';
import {
  addSection,
  emptyReportForm,
  maxReportSearchRunes,
  normalizeReportSearch,
  patchSection,
  removeSection,
  reportFormFrom,
  REPORT_MAX_SECTIONS,
  toReportInput,
  validateReportForm,
  type ReportFormState,
} from '../../apps/console/src/reports/model';
import type { Report } from '../../packages/api-client/src/index';

const analysisA = '44444444-4444-4444-8444-444444444444';
const analysisB = '55555555-5555-4555-8555-555555555555';

function validForm(): ReportFormState {
  return {
    title: '11 月学习行为周报',
    description: '面向教学运营的每周汇总',
    sections: [
      { sectionTitle: ' 第一章 学习概况 ', analysisID: analysisA, analysisVersion: 1 },
      { sectionTitle: '第二章 留存变化', analysisID: analysisB, analysisVersion: 2 },
    ],
  };
}

test('report form validation accepts the bounded valid shape', () => {
  expect(validateReportForm(validForm())).toEqual([]);
});

test('report form validation rejects every domain bound violation', () => {
  const noTitle = validForm();
  noTitle.title = '   ';
  expect(validateReportForm(noTitle).some((message) => message.includes('标题'))).toBe(true);

  const longTitle = validForm();
  longTitle.title = 'x'.repeat(129);
  expect(validateReportForm(longTitle).some((message) => message.includes('128'))).toBe(true);

  const longDescription = validForm();
  longDescription.description = 'y'.repeat(2001);
  expect(validateReportForm(longDescription).some((message) => message.includes('2000'))).toBe(
    true,
  );

  const emptySections = validForm();
  emptySections.sections = [];
  expect(validateReportForm(emptySections).some((message) => message.includes('1 到 50'))).toBe(
    true,
  );

  const missingTitle = validForm();
  missingTitle.sections[0]!.sectionTitle = '';
  expect(validateReportForm(missingTitle).some((message) => message.includes('缺少章节标题'))).toBe(
    true,
  );

  const missingVersion = validForm();
  missingVersion.sections[0]!.analysisVersion = 0;
  expect(
    validateReportForm(missingVersion).some((message) => message.includes('已发布的分析版本')),
  ).toBe(true);

  const duplicate = validForm();
  duplicate.sections[1]!.analysisID = analysisA;
  duplicate.sections[1]!.analysisVersion = 1;
  expect(duplicate.sections.length).toBe(2);
  expect(validateReportForm(duplicate).some((message) => message.includes('重复引用'))).toBe(true);
});

test('section editors stay inside the 1..50 bound', () => {
  let form = emptyReportForm();
  for (let index = 0; index < REPORT_MAX_SECTIONS; index += 1) form = addSection(form);
  expect(form.sections).toHaveLength(REPORT_MAX_SECTIONS);
  expect(addSection(form)).toBe(form);

  const removed = removeSection(form, 0);
  expect(removed.sections).toHaveLength(REPORT_MAX_SECTIONS - 1);

  const patched = patchSection(removed, 0, { sectionTitle: '新标题' });
  expect(patched.sections[0]!.sectionTitle).toBe('新标题');
  expect(removed.sections[0]!.sectionTitle).toBe('');
});

test('toReportInput derives reading order, trims titles and rejects invalid forms', () => {
  const built = toReportInput('development', validForm());
  expect(built.errors).toEqual([]);
  expect(built.input).not.toBeNull();
  expect(built.input!.environment).toBe('development');
  expect(built.input!.definition.title).toBe('11 月学习行为周报');
  expect(built.input!.definition.sections).toEqual([
    {
      reading_order: 1,
      section_title: '第一章 学习概况',
      analysis_id: analysisA,
      analysis_version: 1,
    },
    {
      reading_order: 2,
      section_title: '第二章 留存变化',
      analysis_id: analysisB,
      analysis_version: 2,
    },
  ]);

  const invalid = toReportInput('staging', emptyReportForm());
  expect(invalid.input).toBeNull();
  expect(invalid.errors.length).toBeGreaterThan(0);
});

test('reportFormFrom round-trips the persisted wire shape', () => {
  const report: Report = {
    report_id: '66666666-6666-4666-8666-666666666666',
    project_id: '11111111-1111-4111-8111-111111111111',
    environment: 'production',
    revision: 3,
    definition: {
      title: '归档报告',
      description: '只读快照',
      sections: [
        {
          section_id: '77777777-7777-4777-8777-777777777777',
          reading_order: 1,
          section_title: '概览',
          analysis_id: analysisA,
          analysis_version: 1,
        },
      ],
    },
    created_at: '2026-11-12T00:00:00Z',
    updated_at: '2026-11-12T01:00:00Z',
  };
  const form = reportFormFrom(report);
  expect(form.title).toBe('归档报告');
  expect(form.sections).toEqual([
    { sectionTitle: '概览', analysisID: analysisA, analysisVersion: 1 },
  ]);
  const rebuilt = toReportInput('production', form);
  expect(rebuilt.input!.definition.sections[0]!.reading_order).toBe(1);
});

test('report search terms trim blanks and clamp to the server rune limit', () => {
  expect(normalizeReportSearch('  留存 周报 ')).toBe('留存 周报');
  expect(normalizeReportSearch('   ')).toBe('');
  expect(normalizeReportSearch('')).toBe('');
  const atLimit = '字'.repeat(maxReportSearchRunes);
  expect(normalizeReportSearch(atLimit)).toBe(atLimit);
  const overLimit = '字'.repeat(maxReportSearchRunes + 1);
  const clamped = normalizeReportSearch(overLimit);
  expect([...clamped].length).toBe(maxReportSearchRunes);
  expect(clamped).toBe(atLimit);
  const emojiClamped = normalizeReportSearch(` ${'😀'.repeat(maxReportSearchRunes + 1)} `);
  expect([...emojiClamped].length).toBe(maxReportSearchRunes);
  expect(emojiClamped.startsWith('😀')).toBe(true);
  expect(normalizeReportSearch('a\tb  c')).toBe('a\tb  c');
});
