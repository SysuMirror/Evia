import type { SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Field, Input, Select } from '@evia/design-system';
import type { PublishedField, QueryField } from '../analysis-model';
import { FieldSelect } from './selection-controls';
import type { SectionControlProps } from './types';

type GroupingSectionProps = SectionControlProps & {
  groups: QueryField[];
  interval: SavedAnalysisTemplate['interval'];
  limit: string;
  fields: PublishedField[];
  onGroupsChange: (next: QueryField[]) => void;
  onIntervalChange: (next: SavedAnalysisTemplate['interval']) => void;
  onLimitChange: (next: string) => void;
};
export function GroupingSection({
  id,
  groups,
  interval,
  limit,
  fields,
  onGroupsChange,
  onIntervalChange,
  onLimitChange,
  readOnly,
  disabled,
}: GroupingSectionProps) {
  const blocked = readOnly || disabled;
  return (
    <>
      <div className="field-grid">
        <Field label="时间粒度" htmlFor={`${id}-interval`}>
          <Select
            id={`${id}-interval`}
            disabled={blocked}
            value={interval ?? ''}
            options={[
              { value: '', label: '不按时间分组' },
              { value: 'minute', label: '分钟' },
              { value: 'hour', label: '小时' },
              { value: 'day', label: '天' },
            ]}
            onChange={(event) =>
              onIntervalChange(
                (event.target.value || undefined) as SavedAnalysisTemplate['interval'],
              )
            }
          />
        </Field>
        <Field label="最多结果行数" htmlFor={`${id}-limit`}>
          <Input
            id={`${id}-limit`}
            type="number"
            min={1}
            max={500}
            step={1}
            required
            readOnly={readOnly}
            disabled={disabled}
            value={limit}
            onChange={(event) => onLimitChange(event.target.value)}
          />
        </Field>
      </div>
      {groups.map((field, index) => (
        <div className="saved-analysis-editor-row" key={index}>
          <FieldSelect
            id={id}
            field={field}
            label={`分组 ${index + 1}`}
            fields={fields.filter((item) => !item.type.startsWith('array:'))}
            disabled={blocked}
            onChange={(next) => {
              if (next)
                onGroupsChange(
                  groups.map((item, itemIndex) => (itemIndex === index ? next : item)),
                );
            }}
          />
          {!readOnly && (
            <Button
              disabled={disabled}
              onClick={() => onGroupsChange(groups.filter((_, itemIndex) => itemIndex !== index))}
            >
              移除分组 {index + 1}
            </Button>
          )}
        </div>
      ))}
      {!readOnly && (
        <Button
          disabled={disabled || groups.length >= 3}
          onClick={() => onGroupsChange([...groups, { kind: 'system', key: 'event_key' }])}
        >
          添加分组
        </Button>
      )}
    </>
  );
}
