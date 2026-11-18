import type { EventDefinition, SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Field, Input, Select } from '@evia/design-system';
import { PinControls } from './selection-controls';
import type { SectionControlProps } from './types';

type Attribution = NonNullable<SavedAnalysisTemplate['attribution']>;
type AttributionSectionProps = SectionControlProps & {
  attribution: Attribution | undefined;
  subject: SavedAnalysisTemplate['subject'];
  window: string;
  halfLife: string;
  events: EventDefinition[];
  onAttributionChange: (next: Attribution) => void;
  onSubjectChange: (next: 'uid' | 'anonymous') => void;
  onWindowChange: (next: string) => void;
  onHalfLifeChange: (next: string) => void;
};
export function AttributionSection({
  id,
  attribution,
  subject,
  window,
  halfLife,
  events,
  onAttributionChange,
  onSubjectChange,
  onWindowChange,
  onHalfLifeChange,
  readOnly,
  disabled,
}: AttributionSectionProps) {
  if (!attribution) return null;
  return (
    <>
      <div className="field-grid">
        <Field label="归因主体" htmlFor={`${id}-attribution-subject`}>
          <Select
            id={`${id}-attribution-subject`}
            disabled={readOnly || disabled}
            value={subject ?? 'uid'}
            options={[
              { value: 'uid', label: '原始 UID' },
              { value: 'anonymous', label: '匿名标识' },
            ]}
            onChange={(event) => onSubjectChange(event.target.value as 'uid' | 'anonymous')}
          />
        </Field>
        <Field label="归因模型" htmlFor={`${id}-attribution-model`}>
          <Select
            id={`${id}-attribution-model`}
            disabled={readOnly || disabled}
            value={attribution.model}
            options={[
              { value: 'last_touch', label: '末次触点（窗口内最晚触点）' },
              { value: 'first_touch', label: '首次触点（窗口内最早触点）' },
              { value: 'linear', label: '线性均分（窗内触点等权分摊）' },
              { value: 'position_based', label: '位置归因（首末触点各 40%，中间均分 20%）' },
              { value: 'time_decay', label: '时间衰减（越接近转化权重越高）' },
            ]}
            onChange={(event) =>
              onAttributionChange({
                ...attribution,
                model: event.target.value as Attribution['model'],
              })
            }
          />
        </Field>
        <Field label="回溯窗口（秒，1 至 604800）" htmlFor={`${id}-attribution-window`}>
          <Input
            id={`${id}-attribution-window`}
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
        {attribution.model === 'time_decay' && (
          <Field label="衰减半衰期（秒，1 至 604800）" htmlFor={`${id}-attribution-half-life`}>
            <Input
              id={`${id}-attribution-half-life`}
              type="number"
              min={1}
              max={604800}
              step={1}
              required
              readOnly={readOnly}
              disabled={disabled}
              value={halfLife}
              onChange={(event) => onHalfLifeChange(event.target.value)}
            />
          </Field>
        )}
      </div>
      <div className="saved-analysis-editor-row">
        <PinControls
          id={id}
          pin={attribution.conversion}
          events={events}
          disabled={readOnly || disabled}
          label="转化事件 "
          onChange={(pin) =>
            onAttributionChange({
              ...attribution,
              conversion: { ...attribution.conversion, ...pin },
            })
          }
        />
      </div>
      {attribution.touchpoints.map((step, index) => (
        <div className="saved-analysis-editor-row" key={index}>
          <PinControls
            id={id}
            pin={step}
            events={events}
            disabled={readOnly || disabled}
            label={`触点 ${index + 1} `}
            onChange={(pin) =>
              onAttributionChange({
                ...attribution,
                touchpoints: attribution.touchpoints.map((item, itemIndex) =>
                  itemIndex === index ? { ...item, ...pin } : item,
                ),
              })
            }
          />
          {!readOnly && (
            <Button
              disabled={disabled || attribution.touchpoints.length <= 1}
              onClick={() =>
                onAttributionChange({
                  ...attribution,
                  touchpoints: attribution.touchpoints.filter(
                    (_, itemIndex) => itemIndex !== index,
                  ),
                })
              }
            >
              移除触点 {index + 1}
            </Button>
          )}
        </div>
      ))}
      {!readOnly && (
        <Button
          disabled={disabled || attribution.touchpoints.length >= 8}
          onClick={() =>
            onAttributionChange({
              ...attribution,
              touchpoints: [...attribution.touchpoints, { event_key: '', schema_version: 0 }],
            })
          }
        >
          添加触点
        </Button>
      )}
      <p className="saved-analysis-editor-help">
        触点 1–8
        个且不能与转化事件相同、彼此不能重复；窗口内无触点的转化归入直接来源（__direct__）。
      </p>
    </>
  );
}
