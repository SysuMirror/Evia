import type { SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Field, Select } from '@evia/design-system';
import type { PublishedField } from '../analysis-model';
import { savedMetricLabels, type SavedMetric } from '../saved-analysis-model';
import { FieldSelect } from './selection-controls';
import type { SectionControlProps } from './types';

type MetricsSectionProps = SectionControlProps & {
  metrics: SavedMetric[] | undefined;
  subject: SavedAnalysisTemplate['subject'];
  fields: PublishedField[];
  onMetricsChange: (next: SavedMetric[]) => void;
  onSubjectChange: (next: SavedAnalysisTemplate['subject']) => void;
};
export function MetricsSection({
  id,
  metrics: selectedMetrics,
  subject,
  fields,
  onMetricsChange,
  onSubjectChange,
  readOnly,
  disabled,
}: MetricsSectionProps) {
  const blocked = readOnly || disabled;
  const metrics = selectedMetrics?.length ? selectedMetrics : [{ op: 'count' as const }];
  return (
    <>
      <Field label="身份主体" htmlFor={`${id}-subject`}>
        <Select
          id={`${id}-subject`}
          disabled={blocked}
          value={subject ?? ''}
          options={[
            { value: '', label: '不按身份去重' },
            { value: 'uid', label: '原始 UID' },
            { value: 'anonymous', label: '匿名标识' },
          ]}
          onChange={(event) =>
            onSubjectChange((event.target.value || undefined) as SavedAnalysisTemplate['subject'])
          }
        />
      </Field>
      {metrics.map((metric, index) => {
        const update = (next: SavedMetric) =>
          onMetricsChange(metrics.map((item, itemIndex) => (itemIndex === index ? next : item)));
        return (
          <div className="saved-analysis-editor-row" key={index}>
            <Field label={`指标 ${index + 1} 运算`} htmlFor={`${id}-metric-${index}`}>
              <Select
                id={`${id}-metric-${index}`}
                disabled={blocked}
                value={metric.op}
                options={Object.entries(savedMetricLabels).map(([op, label]) => ({
                  value: op,
                  label,
                }))}
                onChange={(event) => {
                  const op = event.target.value as SavedMetric['op'];
                  update({
                    ...metric,
                    op,
                    ...(op === 'count' ? { field: undefined } : {}),
                    accuracy:
                      op === 'unique' ||
                      op === 'p50' ||
                      op === 'p90' ||
                      op === 'p95' ||
                      op === 'p99'
                        ? metric.accuracy
                        : 'exact',
                  });
                }}
              />
            </Field>
            {metric.op !== 'count' && (
              <FieldSelect
                id={id}
                field={metric.field}
                onChange={(field) => update({ ...metric, field })}
                label={`指标 ${index + 1} 字段`}
                fields={fields.filter((field) =>
                  metric.op === 'unique'
                    ? !field.type.startsWith('array:')
                    : field.type === 'number' || field.type === 'integer',
                )}
                optional={metric.op === 'unique'}
                disabled={blocked}
              />
            )}
            <Field label={`指标 ${index + 1} 精度`} htmlFor={`${id}-accuracy-${index}`}>
              <Select
                id={`${id}-accuracy-${index}`}
                disabled={
                  blocked ||
                  (metric.op !== 'unique' &&
                    metric.op !== 'p50' &&
                    metric.op !== 'p90' &&
                    metric.op !== 'p95' &&
                    metric.op !== 'p99')
                }
                value={metric.accuracy ?? 'exact'}
                options={[
                  { value: 'exact', label: '精确' },
                  { value: 'approximate', label: '近似' },
                ]}
                onChange={(event) =>
                  update({ ...metric, accuracy: event.target.value as 'exact' | 'approximate' })
                }
              />
            </Field>
            {!readOnly && (
              <Button
                disabled={disabled || metrics.length <= 1}
                onClick={() =>
                  onMetricsChange(metrics.filter((_, itemIndex) => itemIndex !== index))
                }
              >
                移除指标 {index + 1}
              </Button>
            )}
          </div>
        );
      })}
      {!readOnly && (
        <Button
          disabled={disabled || (selectedMetrics?.length ?? 1) >= 10}
          onClick={() => onMetricsChange([...metrics, { op: 'count', accuracy: 'exact' }])}
        >
          添加指标
        </Button>
      )}
    </>
  );
}
