import type { EventDefinition, SavedAnalysisTemplate } from '@evia/api-client';
import { Field, Input, Select } from '@evia/design-system';
import { PinControls } from './selection-controls';
import type { SectionControlProps } from './types';

type Retention = NonNullable<SavedAnalysisTemplate['retention']>;
type RetentionSectionProps = SectionControlProps & {
  retention: Retention | undefined;
  subject: SavedAnalysisTemplate['subject'];
  periods: string;
  events: EventDefinition[];
  onRetentionChange: (next: Retention) => void;
  onSubjectChange: (next: 'uid' | 'anonymous') => void;
  onPeriodsChange: (next: string) => void;
};
export function RetentionSection({
  id,
  retention,
  subject,
  periods,
  events,
  onRetentionChange,
  onSubjectChange,
  onPeriodsChange,
  readOnly,
  disabled,
}: RetentionSectionProps) {
  if (!retention) return null;
  return (
    <>
      <div className="field-grid">
        <Field label="留存主体" htmlFor={`${id}-retention-subject`}>
          <Select
            id={`${id}-retention-subject`}
            disabled={readOnly || disabled}
            value={subject ?? 'uid'}
            options={[
              { value: 'uid', label: '原始 UID' },
              { value: 'anonymous', label: '匿名标识' },
            ]}
            onChange={(event) => onSubjectChange(event.target.value as 'uid' | 'anonymous')}
          />
        </Field>
        <Field label="留存粒度" htmlFor={`${id}-retention-granularity`}>
          <Select
            id={`${id}-retention-granularity`}
            disabled={readOnly || disabled}
            value={retention.granularity}
            options={[
              { value: 'day', label: '按天' },
              { value: 'week', label: '按周（周一起算）' },
            ]}
            onChange={(event) =>
              onRetentionChange({
                ...retention,
                granularity: event.target.value as Retention['granularity'],
              })
            }
          />
        </Field>
        <Field label="留存期数（1 至 12）" htmlFor={`${id}-retention-periods`}>
          <Input
            id={`${id}-retention-periods`}
            type="number"
            min={1}
            max={12}
            step={1}
            required
            readOnly={readOnly}
            disabled={disabled}
            value={periods}
            onChange={(event) => onPeriodsChange(event.target.value)}
          />
        </Field>
      </div>
      <div className="saved-analysis-editor-row">
        <PinControls
          id={id}
          pin={retention.start_event}
          events={events}
          disabled={readOnly || disabled}
          label="起始事件 "
          onChange={(pin) =>
            onRetentionChange({ ...retention, start_event: { ...retention.start_event, ...pin } })
          }
        />
      </div>
      <div className="saved-analysis-editor-row">
        <PinControls
          id={id}
          pin={retention.return_event}
          events={events}
          disabled={readOnly || disabled}
          label="回访事件 "
          onChange={(pin) =>
            onRetentionChange({ ...retention, return_event: { ...retention.return_event, ...pin } })
          }
        />
      </div>
      <p className="saved-analysis-editor-help">
        主体在起始事件所在周期进入队列，在后续各期发生回访事件即记为留存；两个事件都固定到具体版本。
      </p>
    </>
  );
}
