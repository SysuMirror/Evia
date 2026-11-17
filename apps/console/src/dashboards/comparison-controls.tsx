import type { ComparisonSpec } from '@evia/api-client';
import { Field, Select } from '@evia/design-system';

type ComparisonControlsProps = {
  id: string;
  value: ComparisonSpec | null;
  disabled: boolean;
  onChange: (spec: ComparisonSpec | null) => void;
};

const UNIT_OPTIONS: { value: string; label: string }[] = [
  { value: 'previous_period', label: '上一相同长度周期' },
  { value: 'day', label: '天（按本地时区）' },
  { value: 'week', label: '周（按本地时区）' },
  { value: 'month', label: '月（按本地时区）' },
  { value: 'year', label: '年（按本地时区）' },
];

/**
 * Comparison selector. "off" means no second window; calendar units require an
 * amount of 1–12, while previous_period mirrors the exact window duration.
 */
export function ComparisonControls({ id, value, disabled, onChange }: ComparisonControlsProps) {
  const unit = value?.unit ?? '';
  const amount = value?.amount ?? 1;
  return (
    <div className="dashboard-comparison-controls" aria-label="比较期设置">
      <Field
        label="比较期"
        htmlFor={`${id}-unit`}
        hint="启用后每个组件会额外运行一个同口径的偏移时间窗。"
      >
        <Select
          id={`${id}-unit`}
          value={unit}
          disabled={disabled}
          onChange={(event) => {
            const next = event.target.value;
            if (!next) {
              onChange(null);
              return;
            }
            onChange(
              next === 'previous_period'
                ? { unit: next as ComparisonSpec['unit'] }
                : { unit: next as ComparisonSpec['unit'], amount: amount || 1 },
            );
          }}
          options={[{ value: '', label: '不比较' }, ...UNIT_OPTIONS]}
        />
      </Field>
      {value && value.unit !== 'previous_period' && (
        <Field label="偏移数量" htmlFor={`${id}-amount`}>
          <Select
            id={`${id}-amount`}
            value={String(value.amount)}
            disabled={disabled}
            onChange={(event) => onChange({ ...value, amount: Number(event.target.value) })}
            options={Array.from({ length: 12 }, (_, index) => {
              const n = index + 1;
              return { value: String(n), label: String(n) };
            })}
          />
        </Field>
      )}
    </div>
  );
}
