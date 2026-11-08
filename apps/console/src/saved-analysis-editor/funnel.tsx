import type { EventDefinition, SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Field, Input, Select } from '@evia/design-system';
import { PinControls } from './selection-controls';
import type { SectionControlProps } from './types';

type Funnel = NonNullable<SavedAnalysisTemplate['funnel']>;
type FunnelSectionProps = SectionControlProps & {
  funnel: Funnel | undefined;
  subject: SavedAnalysisTemplate['subject'];
  window: string;
  events: EventDefinition[];
  onFunnelChange: (next: Funnel) => void;
  onSubjectChange: (next: 'uid' | 'anonymous') => void;
  onWindowChange: (next: string) => void;
};
export function FunnelSection({
  id,
  funnel,
  subject,
  window,
  events,
  onFunnelChange,
  onSubjectChange,
  onWindowChange,
  readOnly,
  disabled,
}: FunnelSectionProps) {
  if (!funnel) return null;
  return (
    <>
      <div className="field-grid">
        <Field label="漏斗主体" htmlFor={`${id}-funnel-subject`}>
          <Select
            id={`${id}-funnel-subject`}
            disabled={readOnly || disabled}
            value={subject ?? 'uid'}
            options={[
              { value: 'uid', label: '原始 UID' },
              { value: 'anonymous', label: '匿名标识' },
            ]}
            onChange={(event) => onSubjectChange(event.target.value as 'uid' | 'anonymous')}
          />
        </Field>
        <Field label="转化窗口（秒）" htmlFor={`${id}-window`}>
          <Input
            id={`${id}-window`}
            type="number"
            min={1}
            max={604800}
            step={1}
            required
            readOnly={readOnly}
            disabled={disabled}
            value={window}
            onChange={(event) => onWindowChange(event.target.value)}
          />
        </Field>
      </div>
      {funnel.steps.map((step, index) => (
        <div className="saved-analysis-editor-row" key={index}>
          <PinControls
            id={id}
            pin={step}
            events={events}
            disabled={readOnly || disabled}
            label={`步骤 ${index + 1} `}
            onChange={(pin) =>
              onFunnelChange({
                ...funnel,
                steps: funnel.steps.map((item, itemIndex) =>
                  itemIndex === index ? { ...item, ...pin } : item,
                ),
              })
            }
          />
          {step.filter && <span className="saved-analysis-editor-help">含步骤筛选</span>}
          {!readOnly && (
            <Button
              disabled={disabled || funnel.steps.length <= 2 || !!funnel.same_entity?.length}
              onClick={() =>
                onFunnelChange({
                  ...funnel,
                  steps: funnel.steps.filter((_, itemIndex) => itemIndex !== index),
                })
              }
            >
              移除步骤 {index + 1}
            </Button>
          )}
        </div>
      ))}
      {!readOnly && (
        <Button
          disabled={disabled || funnel.steps.length >= 8}
          onClick={() =>
            onFunnelChange({
              ...funnel,
              steps: [...funnel.steps, { event_key: '', schema_version: 0 }],
            })
          }
        >
          添加步骤
        </Button>
      )}
      {!!funnel.same_entity?.length && (
        <p className="saved-analysis-editor-help">
          已保留 {funnel.same_entity.length} 条实体关联，存在实体关联时，请在高级 JSON
          中一起调整步骤顺序与关联，避免改变对应关系。
        </p>
      )}
    </>
  );
}
