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
            step_index: '步骤',
            subjects: '到达主体数',
            conversion_rate: '相对首步转化率',
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
      ) : (
        displayCell(row[column.key], column.type, snapshot.body.timezone)
      ),
  }));

  return columns;
}
