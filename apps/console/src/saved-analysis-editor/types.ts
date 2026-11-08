export type EditorSection = 'events' | 'metrics' | 'grouping' | 'filters' | 'funnel' | 'json';
export type SectionNumbers = { limit?: string; window?: string };
export type SectionControlProps = { id: string; readOnly: boolean; disabled: boolean };
export const sectionTitles: Record<EditorSection, string> = {
  events: '固定事件版本',
  metrics: '指标与主体',
  grouping: '分组与粒度',
  filters: '筛选条件',
  funnel: '有序漏斗',
  json: '高级 JSON',
};
