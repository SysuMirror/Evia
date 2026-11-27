import type { QueryResult } from '@evia/api-client';
import { CodeBlock, type DataTableColumn } from '@evia/design-system';
import { formatTime } from '../analysis-model';
import { savedFieldLabel } from '../saved-analysis-model';
import { metricInfo } from './result-labels';
import type { ExecutionSnapshot } from './types';

export function displayCell(value: unknown, type: string, timezone: string) {
  if (value === undefined) return <span>缺失</span>;
  if (value === null) return <span>null</span>;
  if (type === 'datetime')
    return <time dateTime={String(value)}>{formatTime(value, timezone)}</time>;
  if (typeof value === 'boolean') return String(value);
  if (typeof value === 'number')
    return Number.isSafeInteger(value) ? value.toLocaleString('zh-CN') : String(value);
  if (typeof value === 'object')
    return (
      <details>
        <summary>查看内容</summary>
        <CodeBlock>{JSON.stringify(value, null, 2)}</CodeBlock>
      </details>
    );
  return String(value);
}
export function savedResultColumns(
  snapshot: ExecutionSnapshot,
  result: QueryResult,
): DataTableColumn<Record<string, unknown>>[] {
  const kind = snapshot.version.definition.query.kind;
  const metricName = (key: string) =>
    metricInfo(snapshot.version.definition, Number(key.slice(7))).label;
  const groupName = (key: string) => {
    const index = Number(key.match(/^group_(\d+)/)?.[1]);
    const field = snapshot.version.definition.query.group_by?.[index];
    return field ? `${savedFieldLabel(field)}${key.endsWith('_present') ? '，字段存在' : ''}` : key;
  };
  const columns: DataTableColumn<Record<string, unknown>>[] = result.columns.map((column) => ({
    key: column.key,
    header: column.key.startsWith('metric_')
      ? metricName(column.key)
      : column.key.startsWith('group_')
        ? groupName(column.key)
        : ({
            bucket: '时间',
            event_key: '事件',
            event_time: '事件时间',
            uid: '原始 UID',
            anonymous_id: '匿名标识',
            context: '上下文',
            properties: '属性',
            step_index: '步骤',
            subjects: kind === 'attribution' ? '转化主体数' : '到达主体数',
            conversion_rate: '较上一步转化率',
            cohort_index: '队列序号',
            cohort_start: '队列起始',
            cohort_size: '队列规模',
            retained: '各期留存主体数',
            retention_rate: '各期留存率',
            segment_value: '分群标签',
            share: kind === 'attribution' ? '归因占比' : '群体占比',
            path: '事件路径',
            touchpoint: '归因触点',
            credit: '加权权重',
          }[column.key] ??
          column.label ??
          column.key),
    render: (row) =>
      column.key === 'conversion_rate' && typeof row[column.key] === 'number' ? (
        <span title={`原始比例 ${String(row[column.key])}`}>
          {((row[column.key] as number) * 100).toLocaleString('zh-CN', {
            maximumSignificantDigits: 21,
          })}
          %
        </span>
      ) : column.key === 'step_index' && typeof row[column.key] === 'number' ? (
        String((row[column.key] as number) + 1)
      ) : column.key === 'share' && typeof row[column.key] === 'number' ? (
        <span title={`原始比例 ${String(row[column.key])}`}>
          {((row[column.key] as number) * 100).toLocaleString('zh-CN', {
            maximumSignificantDigits: 21,
          })}
          %
        </span>
      ) : column.key === 'credit' && typeof row[column.key] === 'number' ? (
        (row[column.key] as number).toFixed(4).replace(/\.?0+$/, '')
      ) : column.key === 'retention_rate' && Array.isArray(row[column.key]) ? (
        (row[column.key] as unknown[])
          .map((value) =>
            typeof value === 'number'
              ? `${(value * 100).toLocaleString('zh-CN', { maximumSignificantDigits: 21 })}%`
              : String(value),
          )
          .join('，')
      ) : column.key === 'retained' && Array.isArray(row[column.key]) ? (
        (row[column.key] as unknown[]).map((value) => String(value)).join('，')
      ) : column.key === 'path' && Array.isArray(row[column.key]) ? (
        (row[column.key] as unknown[]).map((value) => String(value)).join(' → ')
      ) : (
        displayCell(row[column.key], column.type, snapshot.body.timezone)
      ),
  }));

  return columns;
}
