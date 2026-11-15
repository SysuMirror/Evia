export type EditorSection =
  | 'events'
  | 'metrics'
  | 'grouping'
  | 'filters'
  | 'funnel'
  | 'retention'
  | 'segment'
  | 'path'
  | 'attribution'
  | 'json';
export type SectionNumbers = {
  limit?: string;
  window?: string;
  periods?: string;
  depth?: string;
  halfLife?: string;
};
export type SectionControlProps = { id: string; readOnly: boolean; disabled: boolean };
export const sectionTitles: Record<EditorSection, string> = {
  events: '固定事件版本',
  metrics: '指标与主体',
  grouping: '分组与粒度',
  filters: '筛选条件',
  funnel: '有序漏斗',
  retention: '留存分析',
  segment: '分群分析',
  path: '聚合路径',
  attribution: '归因分析',
  json: '高级 JSON',
};
