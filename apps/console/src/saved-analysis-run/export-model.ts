import type { SavedAnalysisExportInput, SavedAnalysisVersion } from '@evia/api-client';
import { savedExecutionBody } from './time-controls';
import type { SavedRunRange } from './types';

export type SavedExportFormat = 'json' | 'csv' | 'ndjson';

export interface SavedExportFormState {
  name: string;
  format: SavedExportFormat;
}

export const SAVED_EXPORT_NAME_MAX = 200;

export const savedExportFormatOptions: { value: SavedExportFormat; label: string }[] = [
  { value: 'json', label: 'JSON 完整结果文档' },
  { value: 'csv', label: 'CSV 表格（按结果列顺序）' },
  { value: 'ndjson', label: 'NDJSON 每行一条结果记录' },
];

/** Defaults the export name to the immutable published analysis title. */
export function defaultSavedExportForm(version: SavedAnalysisVersion): SavedExportFormState {
  return { name: `${version.definition.title} 导出`, format: 'json' };
}

export function validateSavedExportForm(form: SavedExportFormState): string[] {
  const errors: string[] = [];
  const name = form.name.trim();
  if (!name) {
    errors.push('请填写导出名称。');
  } else if (name.length > SAVED_EXPORT_NAME_MAX) {
    errors.push(`导出名称不能超过 ${SAVED_EXPORT_NAME_MAX} 个字符。`);
  }
  return errors;
}

/**
 * Builds the version export request by reusing the run view's bounded time
 * range validation, so exports and executions share one budget vocabulary.
 * The server rebinds the immutable snapshot; the console never sends a query.
 */
export function buildSavedExportInput(
  range: SavedRunRange,
  kind: SavedAnalysisVersion['definition']['query']['kind'],
  form: SavedExportFormState,
): { input: SavedAnalysisExportInput | null; errors: string[] } {
  const errors = validateSavedExportForm(form);
  let body: ReturnType<typeof savedExecutionBody> | undefined;
  try {
    body = savedExecutionBody(range, kind);
  } catch (failure) {
    errors.push((failure as Error).message);
  }
  if (errors.length > 0 || !body) {
    return { input: null, errors };
  }
  return {
    input: {
      name: form.name.trim(),
      format: form.format,
      time_range: body.time_range,
      timezone: body.timezone,
    },
    errors: [],
  };
}
