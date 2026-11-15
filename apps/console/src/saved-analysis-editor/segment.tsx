import type { EventDefinition, SavedAnalysisTemplate } from '@evia/api-client';
import { Field, Select } from '@evia/design-system';
import type { PublishedField, QueryField } from '../analysis-model';
import { FieldSelect, PinControls } from './selection-controls';
import type { SectionControlProps } from './types';

type Segment = NonNullable<SavedAnalysisTemplate['segment']>;
type SegmentSectionProps = SectionControlProps & {
  segment: Segment | undefined;
  subject: SavedAnalysisTemplate['subject'];
  events: EventDefinition[];
  fields: PublishedField[];
  onSegmentChange: (next: Segment) => void;
  onSubjectChange: (next: 'uid' | 'anonymous') => void;
};
export function SegmentSection({
  id,
  segment,
  subject,
  events,
  fields,
  onSegmentChange,
  onSubjectChange,
  readOnly,
  disabled,
}: SegmentSectionProps) {
  if (!segment) return null;
  return (
    <>
      <div className="field-grid">
        <Field label="分群主体" htmlFor={`${id}-segment-subject`}>
          <Select
            id={`${id}-segment-subject`}
            disabled={readOnly || disabled}
            value={subject ?? 'uid'}
            options={[
              { value: 'uid', label: '原始 UID' },
              { value: 'anonymous', label: '匿名标识' },
            ]}
            onChange={(event) => onSubjectChange(event.target.value as 'uid' | 'anonymous')}
          />
        </Field>
      </div>
      <div className="saved-analysis-editor-row">
        <PinControls
          id={id}
          pin={segment.event}
          events={events}
          disabled={readOnly || disabled}
          label="分群事件 "
          onChange={(pin) => onSegmentChange({ ...segment, event: { ...segment.event, ...pin } })}
        />
      </div>
      <FieldSelect
        id={id}
        label="分群标量字段"
        field={segment.field as QueryField}
        fields={fields}
        disabled={readOnly || disabled}
        onChange={(next) => {
          // FieldSelect speaks the wider QueryField; the segment scalar enum is
          // re-checked by savedAnalysisIssues (system event_time is excluded).
          if (next) onSegmentChange({ ...segment, field: next as Segment['field'] });
        }}
      />
      <p className="saved-analysis-editor-help">
        每个主体按其最早一条分群事件上的该标量字段值唯一归组，缺失值归入固定缺失组；数组与时间类型不作为分群标签。
      </p>
    </>
  );
}
