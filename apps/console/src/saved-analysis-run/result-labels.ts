import type { SavedAnalysisDefinition } from '@evia/api-client';
import { savedFieldLabel } from '../saved-analysis-model';

export function metricInfo(definition: SavedAnalysisDefinition, index: number) {
  const metric = definition.query.metrics?.[index];
  if (!metric) return { label: `指标 ${index + 1}`, unit: undefined };
  const field = metric.field;
  const operation =
    (
      {
        count: '事件数',
        unique: field
          ? '去重值数'
          : `${definition.query.subject === 'anonymous' ? '匿名标识' : '原始 UID'} 去重数`,
        sum: '求和',
        avg: '平均值',
        min: '最小值',
        max: '最大值',
        p95: 'P95',
      } as Record<string, string>
    )[metric.op] ?? metric.op;
  return {
    label: `${operation}${field ? `（${savedFieldLabel(field)}）` : ''}${metric.accuracy === 'approximate' ? '，近似' : ''}`,
    unit: metric.op === 'count' ? '次' : metric.op === 'unique' && !field ? '主体' : undefined,
  };
}
