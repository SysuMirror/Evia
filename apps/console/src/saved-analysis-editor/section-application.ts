import type { SavedAnalysisDefinition } from '@evia/api-client';
import type { PublishedField } from '../analysis-model';
import {
  cloneSavedDefinition,
  flatSavedFilters,
  parseSavedAnalysisDefinition,
  parseSavedFilterValue,
  savedAnalysisIssues,
  savedFieldID,
  savedFilterText,
  type SavedLeafFilter,
} from '../saved-analysis-model';
import type { EditorSection, SectionNumbers } from './types';

type SectionApplication = {
  section: EditorSection;
  candidate: SavedAnalysisDefinition;
  text: string;
  numbers: SectionNumbers;
  filterTexts: string[];
  fields: PublishedField[];
};

/** Apply one local section without validating or rewriting unrelated incomplete sections. */
export function applyEditorSection({
  section,
  candidate,
  text,
  numbers,
  filterTexts,
  fields,
}: SectionApplication): SavedAnalysisDefinition {
  if (section === 'json') return parseSavedAnalysisDefinition(text);
  const next = cloneSavedDefinition(candidate);
  if (section === 'grouping') next.query.limit = integer(numbers.limit ?? '', 1, 500, '结果行数');
  if (section === 'funnel' && next.query.funnel)
    next.query.funnel.window_seconds = integer(numbers.window ?? '', 1, 604800, '转化窗口');
  if (section === 'retention' && next.query.retention)
    next.query.retention.periods = integer(numbers.periods ?? '', 1, 12, '留存期数');
  const leafRows = flatSavedFilters(candidate.query.filter);
  if (section === 'filters' && leafRows) {
    const rows = leafRows.rows.map((filter, index): SavedLeafFilter => {
      if (!('value' in filter)) return filter;
      const input = filterTexts[index] ?? savedFilterText(filter);
      const field = fields.find((item) => item.id === savedFieldID(filter.field));
      if (!field) {
        if (input === savedFilterText(filter)) return filter;
        throw new Error('字段类型尚未加载，请重试字段目录后修改值。');
      }
      return { ...filter, value: parseSavedFilterValue(input, field.type, filter.op) };
    });
    next.query.filter = !rows.length
      ? undefined
      : rows.length === 1 && candidate.query.filter && !('args' in candidate.query.filter)
        ? rows[0]
        : { op: leafRows.join, args: rows };
  }
  const paths: Record<Exclude<EditorSection, 'json'>, string[]> = {
    events: ['/query/event_versions'],
    metrics: ['/query/metrics', '/query/subject'],
    grouping: ['/query/group_by', '/query/interval', '/query/limit'],
    filters: ['/query/filter'],
    funnel: ['/query/funnel', '/query/subject', '/query/filter'],
    retention: ['/query/retention', '/query/subject', '/query/filter'],
  };
  const invalid = savedAnalysisIssues(next).find(
    (issue) =>
      paths[section].some((path) => issue.path.startsWith(path)) ||
      ((section === 'events' || section === 'funnel') && issue.message.includes('固定版本')),
  );
  if (invalid) throw new Error(`${invalid.path}，${invalid.message}`);
  return next;
}
function integer(text: string, minimum: number, maximum: number, label: string): number {
  const value = Number(text);
  if (!text.trim() || !Number.isSafeInteger(value) || value < minimum || value > maximum)
    throw new Error(`${label}须为 ${minimum} 至 ${maximum} 的整数。`);
  return value;
}
