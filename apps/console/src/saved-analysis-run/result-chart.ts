import type { QueryResult, SavedAnalysisExecute, SavedAnalysisVersion } from '@evia/api-client';
import type { SavedAnalysisChartProps } from '@evia/design-system/charts';
import { formatTime } from '../analysis-model';
import { savedFieldLabel } from '../saved-analysis-model';
import { metricInfo } from './result-labels';

export function savedAnalysisResultChart(
  version: SavedAnalysisVersion,
  result: QueryResult,
  execution: SavedAnalysisExecute,
): SavedAnalysisChartProps | undefined {
  const definition = version.definition,
    kind = definition.visualization.kind;
  if (kind === 'metric' || kind === 'table') return undefined;
  const finite = (value: unknown) =>
    typeof value === 'number' && Number.isFinite(value) ? value : null;
  if (kind === 'funnel') {
    const rows = [...result.rows].sort(
      (left, right) => Number(left.step_index) - Number(right.step_index),
    );
    return {
      kind,
      label: definition.title,
      timezone: execution.timezone,
      categories: rows.map((row, index) => {
        const step = definition.query.funnel?.steps[Number(row.step_index)];
        return {
          key: String(row.step_index),
          label: `${Number(row.step_index) + 1}. ${step?.event_key ?? String(row.event_key ?? '')}${step ? ` v${step.schema_version}` : ''}`,
        };
      }),
      series: [
        {
          id: 'subjects',
          label: '到达主体数',
          unit: '主体',
          values: rows.map((row) => finite(row.subjects)),
        },
      ],
    };
  }
  const metricColumns = result.columns.filter((column) => /^metric_\d+$/.test(column.key));
  const groupColumns = result.columns.filter((column) => /^group_\d+$/.test(column.key));
  const group = (row: Record<string, unknown>) =>
    groupColumns.map((column) => {
      const present = row[`${column.key}_present`],
        value = row[column.key];
      const label = present === false || value === undefined ? '缺失' : JSON.stringify(value);
      const field = definition.query.group_by?.[Number(column.key.slice(6))];
      return {
        key: [column.key, present === false ? 'missing' : 'present', typeof value, value],
        valueLabel: label,
        label: `${field ? savedFieldLabel(field) : (column.label ?? column.key)}=${label}`,
      };
    });
  const groupKey = (row: Record<string, unknown>) =>
    JSON.stringify(group(row).map((item) => item.key));
  const groupLabel = (row: Record<string, unknown>) => {
    const fields = group(row);
    return fields.length === 1
      ? fields[0]!.valueLabel
      : fields.map((item) => item.label).join('，');
  };
  if (definition.query.interval) {
    const times = [...new Set(result.rows.map((row) => String(row.bucket)))].sort(
      (left, right) => Date.parse(left) - Date.parse(right),
    );
    const groups = new Map<string, { label: string; rows: Record<string, unknown>[] }>();
    for (const row of result.rows) {
      const key = groupKey(row);
      const item = groups.get(key) ?? { label: groupLabel(row), rows: [] };
      item.rows.push(row);
      groups.set(key, item);
    }
    const series: SavedAnalysisChartProps['series'][number][] = [];
    for (const [groupID, item] of groups)
      for (const metric of metricColumns) {
        const values = new Map(
          item.rows.map((row) => [String(row.bucket), finite(row[metric.key])]),
        );
        const info = metricInfo(definition, Number(metric.key.slice(7)));
        series.push({
          id: `${groupID}:${metric.key}`,
          label: `${item.label ? `${item.label}，` : ''}${info.label}`,
          unit: info.unit,
          values: times.map((time) => values.get(time) ?? null),
        });
      }
    return {
      kind: kind === 'bar' ? 'bar' : 'line',
      label: definition.title,
      timezone: execution.timezone,
      categories: times.map((time) => ({
        key: time,
        label: formatTime(time, execution.timezone),
        timestamp: Date.parse(time),
      })),
      series,
      timeRange: {
        from: Date.parse(execution.time_range.from),
        to: Date.parse(execution.time_range.to),
      },
      intervalMs: { minute: 60000, hour: 3600000, day: 86400000 }[definition.query.interval],
    };
  }
  return {
    kind: 'bar',
    label: definition.title,
    timezone: execution.timezone,
    categories: result.rows.map((row, index) => ({
      key: groupKey(row) || String(index),
      label: groupLabel(row) || '整段范围',
    })),
    series: metricColumns.map((metric) => ({
      ...metricInfo(definition, Number(metric.key.slice(7))),
      id: metric.key,
      values: result.rows.map((row) => finite(row[metric.key])),
    })),
  };
}
