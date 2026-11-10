import { useId } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type Environment, type MetricReference } from '@evia/api-client';
import { Field, Select } from '@evia/design-system';

type MetricReferenceSectionProps = {
  projectID: string;
  environment: Environment;
  actorID?: string;
  value: MetricReference;
  readOnly: boolean;
  disabled: boolean;
  onChange: (ref: MetricReference) => void;
};

/**
 * Picks an immutable published metric version as the data source. Only metrics
 * with at least one published version are offered; the explicit version is
 * always stored so a later metric publication never changes this analysis.
 */
export function MetricReferenceSection({
  projectID,
  environment,
  actorID,
  value,
  readOnly,
  disabled,
  onChange,
}: MetricReferenceSectionProps) {
  const id = useId();
  const scope = actorID ?? 'anonymous';
  const metrics = useQuery({
    queryKey: ['metrics', projectID, scope, environment, 'saved-analysis-picker'],
    queryFn: ({ signal }) => api.listMetrics(projectID, { environment, limit: 100 }, signal),
    enabled: !readOnly || !!value.metric_id,
  });
  const published = (metrics.data?.items ?? []).filter((item) => item.published_version > 0);
  const selectedMetric = value.metric_id || '';
  const versions = useQuery({
    queryKey: ['metrics', projectID, scope, selectedMetric, 'versions'],
    queryFn: ({ signal }) =>
      api.listMetricVersions(projectID, selectedMetric, { limit: 50 }, signal),
    enabled: !!selectedMetric,
  });
  const metricOptions = [
    { value: '', label: metrics.isLoading ? '加载指标中…' : '请选择已发布指标' },
    ...published.map((item) => ({
      value: item.metric_id,
      label: `${item.title}（已发布 v${item.published_version}）`,
    })),
  ];
  const versionItems = [...(versions.data?.items ?? [])].sort((a, b) => b.version - a.version);
  const versionOptions = [
    { value: '', label: versions.isLoading ? '加载版本中…' : '请选择版本' },
    ...versionItems.map((item) => ({ value: String(item.version), label: `v${item.version}` })),
  ];
  return (
    <fieldset className="metric-reference-section section-stack" disabled={readOnly || disabled}>
      <legend>指标数据源</legend>
      <p className="field-help">
        固定引用一个已发布的不可变指标版本，保存分析将以整段指标卡片运行；指标后续发布新版本不会改变本分析。
      </p>
      <Field label="已发布指标" htmlFor={`${id}-metric`} required>
        <Select
          id={`${id}-metric`}
          value={selectedMetric}
          disabled={readOnly || disabled || metrics.isLoading}
          options={metricOptions}
          onChange={(event) => onChange({ metric_id: event.target.value, version: 0 })}
        />
      </Field>
      {metrics.isError && (
        <p role="alert" className="saved-analysis-editor-errors">
          指标列表加载失败，请重试。
        </p>
      )}
      {!metrics.isLoading && !metrics.isError && published.length === 0 && (
        <p className="saved-analysis-editor-help">
          当前环境还没有已发布指标，请先在「独立指标」中发布一个指标。
        </p>
      )}
      <Field label="指标版本" htmlFor={`${id}-version`} required>
        <Select
          id={`${id}-version`}
          value={value.version ? String(value.version) : ''}
          disabled={readOnly || disabled || !selectedMetric || versions.isLoading}
          options={versionOptions}
          onChange={(event) =>
            onChange({ metric_id: selectedMetric, version: Number(event.target.value) })
          }
        />
      </Field>
      {versions.isError && (
        <p role="alert" className="saved-analysis-editor-errors">
          指标版本加载失败，请重试。
        </p>
      )}
    </fieldset>
  );
}
