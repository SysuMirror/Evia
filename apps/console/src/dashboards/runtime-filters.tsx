import { useEffect, useId, useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import {
  api,
  type DashboardExecute,
  type DashboardVersion,
  type MetricVersion,
  type SavedAnalysisVersion,
} from '@evia/api-client';
import { Alert, Button, Field, Input, Modal, Select } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import {
  fieldOperators,
  operators,
  publishedFields,
  systemFields,
  type QueryFilter,
  type PublishedField,
} from '../analysis-model';
import { parseSavedFilterValue, savedFieldID, savedPins } from '../saved-analysis-model';
import { CatalogError } from '../catalog-helpers';

type Rule = { widget_id: string; field: string; op: string; text: string };
export function DashboardRuntimeFilters({
  context,
  version,
  analyses,
  metrics,
  open,
  disabled,
  onClose,
  onApply,
  onPendingChange,
  onDenied,
}: {
  context: CatalogPageProps;
  version: DashboardVersion;
  analyses: ReadonlyMap<string, SavedAnalysisVersion>;
  metrics: ReadonlyMap<string, MetricVersion>;
  open: boolean;
  disabled: boolean;
  onClose: () => void;
  onApply: (filters: NonNullable<DashboardExecute['widget_filters']>) => void;
  onPendingChange: (pending: boolean) => void;
  onDenied: (error: Error) => void;
}) {
  const id = useId(),
    [rows, setRows] = useState<Rule[]>([]),
    [baseline, setBaseline] = useState('[]'),
    [error, setError] = useState('');
  // Pinned event versions come from both saved-analysis queries and atomic
  // metric definitions; runtime filters may only target those immutable pins.
  const pins = [
    ...new Map(
      [
        ...[...analyses.values()].flatMap((a) => savedPins(a.definition.query)),
        ...[...metrics.values()].flatMap((m) => m.definition.event_versions),
      ].map((p) => [`${p.event_key}:${p.schema_version}`, p]),
    ).values(),
  ];
  const catalog = useQueries({
    queries: pins.map((pin) => ({
      queryKey: [
        'dashboards',
        context.project.id,
        context.environment,
        context.session.user_id,
        'filter-fields',
        pin.event_key,
        pin.schema_version,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.getEventVersion(context.project.id, pin.event_key, pin.schema_version, signal),
      enabled: open,
    })),
  });
  const allFields: PublishedField[] = [
    ...systemFields.map((f) => ({
      id: `system:${f.key}`,
      label: f.label,
      type: f.type,
      reference: { kind: 'system' as const, key: f.key },
    })),
    ...catalog.flatMap((item, index) =>
      item.data
        ? publishedFields(
            item.data.definition.schema,
            pins[index]!.event_key,
            pins[index]!.schema_version,
          )
        : [],
    ),
  ];
  const changed = JSON.stringify(rows) !== baseline;
  useEffect(() => onPendingChange(changed), [changed, onPendingChange]);
  useEffect(() => {
    for (const result of catalog)
      if (
        result.error &&
        'status' in result.error &&
        [401, 403].includes(Number(result.error.status))
      )
        onDenied(result.error);
  }, [catalog.map((c) => c.error).join(), onDenied]);
  function fields(widgetID: string) {
    const w = version.definition.widgets.find((w) => w.widget_id === widgetID);
    let sourcePins: { event_key: string; schema_version: number }[] = [];
    if (w?.metric_ref) {
      sourcePins =
        metrics.get(`${w.metric_ref.metric_id}:${w.metric_ref.version}`)?.definition
          .event_versions ?? [];
    } else if (w) {
      const analysis = analyses.get(`${w.analysis_id}:${w.analysis_version}`);
      if (analysis) sourcePins = savedPins(analysis.definition.query);
    }
    const allowed = new Set(sourcePins.map((p) => `${p.event_key}:${p.schema_version}`));
    return allFields.filter(
      (f) =>
        f.reference.kind === 'system' ||
        allowed.has(`${f.reference.event_key}:${f.reference.schema_version}`),
    );
  }
  function patch(index: number, value: Partial<Rule>) {
    setRows((items) => items.map((item, i) => (i === index ? { ...item, ...value } : item)));
    setError('');
  }
  return (
    <Modal
      open={open}
      title="组件运行筛选"
      onRequestClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>返回</Button>
          <Button
            disabled={disabled || !changed}
            onClick={() => {
              setRows(JSON.parse(baseline) as Rule[]);
              setError('');
            }}
          >
            放弃未应用筛选
          </Button>
          <Button
            variant="primary"
            type="submit"
            form={`${id}-form`}
            disabled={disabled || catalog.some((c) => c.isFetching)}
          >
            应用筛选
          </Button>
        </>
      }
    >
      <form
        id={`${id}-form`}
        className="section-stack"
        onSubmit={(e) => {
          e.preventDefault();
          try {
            const grouped = new Map<string, QueryFilter[]>();
            for (const row of rows) {
              const field = fields(row.widget_id).find((f) => f.id === row.field);
              if (!field) throw new Error('请选择目标组件和已发布字段。');
              const filter: QueryFilter =
                row.op === 'is_present' || row.op === 'is_missing'
                  ? { op: row.op, field: field.reference }
                  : {
                      op: row.op as 'eq',
                      field: field.reference,
                      value: parseSavedFilterValue(row.text, field.type, row.op as 'eq'),
                    };
              grouped.set(row.widget_id, [...(grouped.get(row.widget_id) ?? []), filter]);
            }
            onApply(
              [...grouped].map(([widget_id, args]) => ({
                widget_id,
                filter: args.length === 1 ? args[0]! : { op: 'and', args },
              })),
            );
            setBaseline(JSON.stringify(rows));
            setError('');
            onClose();
          } catch (failure) {
            setError((failure as Error).message);
          }
        }}
      >
        <p className="field-help">
          每条条件只作用于选定组件，同组件条件同时满足，并与组件自身的固定筛选同时生效。调整不会执行查询。
        </p>
        {error && <Alert tone="error" title={error} />}
        {catalog.map((r, i) => (
          <CatalogError key={i} error={r.error} retry={() => void r.refetch()} />
        ))}
        {rows.map((row, index) => {
          const options = fields(row.widget_id),
            field = options.find((f) => f.id === row.field);
          return (
            <fieldset
              key={index}
              className="section-stack dashboard-filter-row"
              disabled={disabled}
            >
              <legend>条件 {index + 1}</legend>
              <Field label={`条件 ${index + 1} 目标组件`} htmlFor={`${id}-${index}-widget`}>
                <Select
                  id={`${id}-${index}-widget`}
                  value={row.widget_id}
                  options={[
                    { value: '', label: '选择目标组件' },
                    ...version.definition.widgets
                      // Presentation-only text cards have no query fields to filter.
                      .filter((w) => !w.text_card)
                      .map((w) => ({
                        value: w.widget_id,
                        label: `${w.title}（${w.metric_ref ? '指标' : '分析'}）`,
                      })),
                  ]}
                  onChange={(e) =>
                    patch(index, { widget_id: e.target.value, field: '', op: 'eq', text: '' })
                  }
                />
              </Field>
              <Field label={`条件 ${index + 1} 字段`} htmlFor={`${id}-${index}-field`}>
                <Select
                  id={`${id}-${index}-field`}
                  value={row.field}
                  options={[
                    { value: '', label: '选择已发布字段' },
                    ...options.map((f) => ({ value: f.id, label: f.label })),
                  ]}
                  onChange={(e) => patch(index, { field: e.target.value, op: 'eq', text: '' })}
                />
              </Field>
              <Field label={`条件 ${index + 1} 运算`} htmlFor={`${id}-${index}-op`}>
                <Select
                  id={`${id}-${index}-op`}
                  value={row.op}
                  options={(field ? fieldOperators(field.reference.key, field.type) : ['eq']).map(
                    (op) => ({ value: op, label: operators[op] ?? op }),
                  )}
                  onChange={(e) => patch(index, { op: e.target.value })}
                />
              </Field>
              {!['is_present', 'is_missing'].includes(row.op) && (
                <Field
                  label={`条件 ${index + 1} 值`}
                  htmlFor={`${id}-${index}-value`}
                  hint={
                    field?.type === 'boolean'
                      ? 'true 或 false'
                      : row.op === 'in' || row.op === 'not_in'
                        ? '输入 JSON 数组'
                        : undefined
                  }
                >
                  <Input
                    id={`${id}-${index}-value`}
                    value={row.text}
                    onChange={(e) => patch(index, { text: e.target.value })}
                  />
                </Field>
              )}
              <Button
                onClick={() => {
                  setRows((items) => items.filter((_, i) => i !== index));
                  setError('');
                }}
              >
                移除条件 {index + 1}
              </Button>
            </fieldset>
          );
        })}
        <Button
          disabled={disabled || rows.length >= 20}
          onClick={() =>
            setRows((items) => [...items, { widget_id: '', field: '', op: 'eq', text: '' }])
          }
        >
          添加运行筛选
        </Button>
      </form>
    </Modal>
  );
}
