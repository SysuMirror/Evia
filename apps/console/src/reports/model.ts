import type {
  Report,
  ReportInput,
  ReportSectionInput,
  SavedAnalysisVersion,
} from '@evia/api-client';

export const REPORT_TITLE_MAX = 128;
export const REPORT_DESCRIPTION_MAX = 2000;
export const REPORT_SECTION_TITLE_MAX = 128;
export const REPORT_MAX_SECTIONS = 50;

/** Mirrors the server-side reports.MaxTitleLength rune boundary for the q term. */
export const maxReportSearchRunes = 128;

/**
 * Normalizes the title search box before the term reaches the API: trims
 * surrounding whitespace and clamps to the server rune limit so an over-long
 * local input can never trigger a guaranteed 400. A blank result means "no
 * filter", which the list view encodes by omitting the q parameter.
 */
export function normalizeReportSearch(input: string): string {
  const trimmed = input.trim();
  const runes = [...trimmed];
  return runes.length > maxReportSearchRunes
    ? runes.slice(0, maxReportSearchRunes).join('')
    : trimmed;
}

/** One editor row. reading_order is derived from the row position on submit. */
export interface SectionForm {
  sectionTitle: string;
  analysisID: string;
  analysisVersion: number;
}

export interface ReportFormState {
  title: string;
  description: string;
  sections: SectionForm[];
}

export function emptyReportForm(): ReportFormState {
  return {
    title: '',
    description: '',
    sections: [{ sectionTitle: '', analysisID: '', analysisVersion: 0 }],
  };
}

export function reportFormFrom(report: Report): ReportFormState {
  return {
    title: report.definition.title,
    description: report.definition.description,
    sections: report.definition.sections.map((section) => ({
      sectionTitle: section.section_title,
      analysisID: section.analysis_id,
      analysisVersion: section.analysis_version,
    })),
  };
}

export function addSection(form: ReportFormState): ReportFormState {
  if (form.sections.length >= REPORT_MAX_SECTIONS) return form;
  return {
    ...form,
    sections: [...form.sections, { sectionTitle: '', analysisID: '', analysisVersion: 0 }],
  };
}

export function removeSection(form: ReportFormState, index: number): ReportFormState {
  return { ...form, sections: form.sections.filter((_, current) => current !== index) };
}

export function patchSection(
  form: ReportFormState,
  index: number,
  patch: Partial<SectionForm>,
): ReportFormState {
  return {
    ...form,
    sections: form.sections.map((section, current) =>
      current === index ? { ...section, ...patch } : section,
    ),
  };
}

/**
 * Console-side mirrors of the server bounds in internal/reports. The service
 * and PostgreSQL foreign keys stay authoritative; this only keeps obvious
 * mistakes out of the request.
 */
export function validateReportForm(form: ReportFormState): string[] {
  const errors: string[] = [];
  const title = form.title.trim();
  if (!title) {
    errors.push('请填写报告标题。');
  } else if (title.length > REPORT_TITLE_MAX) {
    errors.push(`报告标题不能超过 ${REPORT_TITLE_MAX} 个字符。`);
  }
  if (form.description.length > REPORT_DESCRIPTION_MAX) {
    errors.push(`报告描述不能超过 ${REPORT_DESCRIPTION_MAX} 个字符。`);
  }
  if (form.sections.length < 1 || form.sections.length > REPORT_MAX_SECTIONS) {
    errors.push(`报告必须包含 1 到 ${REPORT_MAX_SECTIONS} 个章节。`);
    return errors;
  }
  const pinned = new Set<string>();
  form.sections.forEach((section, index) => {
    const order = index + 1;
    const sectionTitle = section.sectionTitle.trim();
    if (!sectionTitle) {
      errors.push(`第 ${order} 章缺少章节标题。`);
    } else if (sectionTitle.length > REPORT_SECTION_TITLE_MAX) {
      errors.push(`第 ${order} 章标题不能超过 ${REPORT_SECTION_TITLE_MAX} 个字符。`);
    }
    if (!section.analysisID || section.analysisVersion < 1) {
      errors.push(`第 ${order} 章必须选择一个已发布的分析版本。`);
      return;
    }
    const key = `${section.analysisID}#${section.analysisVersion}`;
    if (pinned.has(key)) {
      errors.push(`第 ${order} 章与其他章节重复引用了同一分析版本。`);
    }
    pinned.add(key);
  });
  return errors;
}

export function toReportInput(
  environment: ReportInput['environment'],
  form: ReportFormState,
): { input: ReportInput | null; errors: string[] } {
  const errors = validateReportForm(form);
  if (errors.length > 0) return { input: null, errors };
  const sections: ReportSectionInput[] = form.sections.map((section, index) => ({
    reading_order: index + 1,
    section_title: section.sectionTitle.trim(),
    analysis_id: section.analysisID,
    analysis_version: section.analysisVersion,
  }));
  return {
    input: {
      environment,
      definition: {
        title: form.title.trim(),
        description: form.description,
        sections,
      },
    },
    errors: [],
  };
}

export function versionLabel(version: SavedAnalysisVersion): string {
  return `v${version.version}，${version.definition.title}`;
}

export function formatInstant(value: string | null | undefined): string {
  if (!value) return '—';
  const instant = new Date(value);
  if (Number.isNaN(instant.getTime())) return value;
  return instant
    .toISOString()
    .replace('T', ' ')
    .replace(/\.\d+Z$/, ' UTC');
}
