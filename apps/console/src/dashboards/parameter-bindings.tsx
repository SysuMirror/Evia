import { useQueries, useQuery } from '@tanstack/react-query';
import {
  api,
  type DashboardWidget,
  type ParameterBinding,
  type ParameterDeclaration,
} from '@evia/api-client';
import { Button, Field, Select } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { publishedFields, systemFields, type PublishedField } from '../analysis-model';
import { savedFieldID, savedPins } from '../saved-analysis-model';

/**
 * Bindings may only target fields owned by this widget's own pinned source.
 * The catalog fetch mirrors the runtime filter modal: resolve the immutable
 * analysis/metric version, then each pinned event version's published schema.
 */
export function WidgetParameterBindings({
  context,
  widget,
  parameters,
  value,
  onChange,
  disabled,
}: {
  context: CatalogPageProps;
  widget: DashboardWidget;
  parameters: readonly ParameterDeclaration[];
  value: ParameterBinding[];
  onChange: (bindings: ParameterBinding[]) => void;
  disabled: boolean;
}) {
  const isMetric = !!widget.metric_ref;
  const analysisID = widget.analysis_id;
  const analysisVersion = widget.analysis_version;
  const metricID = widget.metric_ref?.metric_id;
  const metricVersion = widget.metric_ref?.version;

  const analysis = useQuery({
    queryKey: [
      'dashboards',
      context.project.id,
      context.environment,
      context.session.user_id,
      'binding-analysis',
      analysisID,
      analysisVersion,
    ],
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      api.getSavedAnalysisVersion(context.project.id, analysisID!, analysisVersion!, signal),
    enabled: !isMetric && !!analysisID && !!analysisVersion,
  });
  const metric = useQuery({
    queryKey: [
      'dashboards',
      context.project.id,
      context.environment,
      context.session.user_id,
      'binding-metric',
      metricID,
      metricVersion,
    ],
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      api.getMetricVersion(context.project.id, metricID!, metricVersion!, signal),
    enabled: isMetric && !!metricID && !!metricVersion,
  });
  const pins = isMetric
    ? (metric.data?.definition.event_versions ?? [])
    : analysis.data
      ? savedPins(analysis.data.definition.query)
      : [];
  const catalogs = useQueries({
    queries: pins.map((pin) => ({
      queryKey: [
        'dashboards',
        context.project.id,
        context.environment,
        context.session.user_id,
        'binding-fields',
        pin.event_key,
        pin.schema_version,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.getEventVersion(context.project.id, pin.event_key, pin.schema_version, signal),
    })),
  });
  const fields: PublishedField[] = [
    ...systemFields.map((f) => ({
      id: `system:${f.key}`,
      label: f.label,
      type: f.type,
      reference: { kind: 'system' as const, key: f.key },
    })),
    ...catalogs.flatMap((item, index) =>
      item.data
        ? publishedFields(
            item.data.definition.schema,
            pins[index]!.event_key,
            pins[index]!.schema_version,
          )
        : [],
    ),
  ];
  const fieldByID = new Map(fields.map((field) => [savedFieldID(field.reference), field]));
  const boundParameters = new Set(value.map((binding) => binding.parameter_id));
  const availableParameters = parameters.filter((decl) => !boundParameters.has(decl.parameter_id));

  function patch(index: number, next: ParameterBinding) {
    onChange(value.map((binding, i) => (i === index ? next : binding)));
  }

  return (
    <fieldset disabled={disabled} className="section-stack dashboard-binding-editor">
      <legend>参数绑定（{value.length}）</legend>
      <p className="field-help">
        每条绑定把一个看板参数映射到该组件固定来源的字段；相同参数在一个组件内只能绑定一次。
      </p>
      {parameters.length === 0 && <p className="field-help">请先在看板参数区声明参数。</p>}
      {value.map((binding, index) => (
        <div key={index} className="dashboard-binding-row">
          <Field label="参数" htmlFor={`binding-${index}-parameter`}>
            <Select
              id={`binding-${index}-parameter`}
              value={binding.parameter_id}
              options={[
                { value: binding.parameter_id, label: binding.parameter_id },
                ...availableParameters.map((decl) => ({
                  value: decl.parameter_id,
                  label: `${decl.label}（${decl.parameter_id}）`,
                })),
              ]}
              onChange={(event) => patch(index, { ...binding, parameter_id: event.target.value })}
            />
          </Field>
          <Field label="绑定字段" htmlFor={`binding-${index}-field`}>
            <Select
              id={`binding-${index}-field`}
              value={savedFieldID(binding.field)}
              options={fields.map((field) => ({
                value: savedFieldID(field.reference),
                label: field.label,
              }))}
              onChange={(event) => {
                const selected = fieldByID.get(event.target.value);
                if (selected) patch(index, { ...binding, field: selected.reference });
              }}
            />
          </Field>
          <Button variant="danger" onClick={() => onChange(value.filter((_, i) => i !== index))}>
            移除绑定
          </Button>
        </div>
      ))}
      <Button
        disabled={parameters.length === 0 || availableParameters.length === 0}
        onClick={() => {
          const decl = availableParameters[0];
          const field = fields[0];
          if (!decl || !field) return;
          onChange([...value, { parameter_id: decl.parameter_id, field: field.reference }]);
        }}
      >
        添加绑定
      </Button>
    </fieldset>
  );
}
