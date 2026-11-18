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

type SourceKind = 'atomic' | 'composite';

function sourceOf(ref: MetricReference): SourceKind {
  return ref.kind === 'composite' ? 'composite' : 'atomic';
}

/**
 * Picks an immutable published metric version as the data source. Atomic
 * references resolve metric_versions and omit the kind field (historical
 * byte shape); composite references carry kind="composite" and resolve
 * formula_metric_versions through the query worker. Only metrics with at least
 * one published version are offered; the explicit version is always stored so a
 * later publication never changes this analysis.
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
  const source = sourceOf(value);
  const atomic = useQuery({
    queryKey: ['metrics', projectID, scope, environment, 'saved-analysis-picker'],
    queryFn: ({ signal }) => api.listMetrics(projectID, { environment, limit: 100 }, signal),
    enabled: source === 'atomic' && (!readOnly || !!value.metric_id),
  });
  const composite = useQuery({
    queryKey: ['formula-metrics', projectID, scope, environment, 'saved-analysis-picker'],
    queryFn: ({ signal }) => api.listFormulaMetrics(projectID, { environment, limit: 100 }, signal),
    enabled: source === 'composite' && (!readOnly || !!value.metric_id),
  });
  const isLoading = composite.isLoading || atomic.isLoading;
  const isError = composite.isError || atomic.isError;
  // Normalize the two distinct record shapes into the picker's local shape so
  // the Select never depends on the divergent definition types.
  const published: Array<{ metric_id: string; title: string; published_version: number }> =
    source === 'composite'
      ? (composite.data?.items ?? [])
          .filter((item) => item.published_version > 0)
          .map((item) => ({
            metric_id: item.metric_id,
            title: item.title,
            published_version: item.published_version,
          }))
      : (atomic.data?.items ?? [])
          .filter((item) => item.published_version > 0)
          .map((item) => ({
            metric_id: item.metric_id,
            title: item.title,
            published_version: item.published_version,
          }));
  const selectedMetric = value.metric_id || '';
  const atomicVersions = useQuery({
    queryKey: ['metric-versions', projectID, scope, selectedMetric],
    queryFn: ({ signal }) =>
      api.listMetricVersions(projectID, selectedMetric, { limit: 50 }, signal),
    enabled: !!selectedMetric && source === 'atomic',
  });
  const compositeVersions = useQuery({
    queryKey: ['formula-metric-versions', projectID, scope, selectedMetric],
    queryFn: ({ signal }) =>
      api.listFormulaMetricVersions(projectID, selectedMetric, { limit: 50 }, signal),
    enabled: !!selectedMetric && source === 'composite',
  });
  const versions = source === 'composite' ? compositeVersions : atomicVersions;
  const buildRef = (metricID: string, version: number): MetricReference =>
    source === 'composite'
      ? { metric_id: metricID, version, kind: 'composite' }
      : { metric_id: metricID, version };
  const metricOptions = [
    { value: '', label: isLoading ? '加载指标中…' : '请选择已发布指标' },
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
      <Field label="指标类型" htmlFor={`${id}-source`} required>
        <Select
          id={`${id}-source`}
          value={source}
          disabled={readOnly || disabled}
          options={[
            { value: 'atomic', label: '独立指标（原子）' },
            { value: 'composite', label: '复合公式指标' },
          ]}
          onChange={(event) => {
            const next: SourceKind = event.target.value === 'composite' ? 'composite' : 'atomic';
            // Switching source resets the pinned id/version; an atomic ref keeps
            // no kind field while a composite ref carries the explicit kind.
            onChange(
              next === 'composite'
                ? { metric_id: '', version: 0, kind: 'composite' }
                : { metric_id: '', version: 0 },
            );
          }}
        />
      </Field>
      <Field label="已发布指标" htmlFor={`${id}-metric`} required>
        <Select
          id={`${id}-metric`}
          value={selectedMetric}
          disabled={readOnly || disabled || isLoading}
          options={metricOptions}
          onChange={(event) => onChange(buildRef(event.target.value, 0))}
        />
      </Field>
      {isError && (
        <p role="alert" className="saved-analysis-editor-errors">
          指标列表加载失败，请重试。
        </p>
      )}
      {!isLoading && !isError && published.length === 0 && (
        <p className="saved-analysis-editor-help">
          当前环境还没有已发布{source === 'composite' ? '复合公式' : ''}指标，请先发布一个指标。
        </p>
      )}
      <Field label="指标版本" htmlFor={`${id}-version`} required>
        <Select
          id={`${id}-version`}
          value={value.version ? String(value.version) : ''}
          disabled={readOnly || disabled || !selectedMetric || versions.isLoading}
          options={versionOptions}
          onChange={(event) => onChange(buildRef(selectedMetric, Number(event.target.value)))}
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
