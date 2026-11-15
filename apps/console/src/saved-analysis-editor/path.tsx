import type { EventDefinition, SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Checkbox, Field, Input, Select } from '@evia/design-system';
import { PinControls } from './selection-controls';
import type { SectionControlProps } from './types';

type Path = NonNullable<SavedAnalysisTemplate['path']>;
type PathSectionProps = SectionControlProps & {
  path: Path | undefined;
  subject: SavedAnalysisTemplate['subject'];
  depth: string;
  events: EventDefinition[];
  onPathChange: (next: Path) => void;
  onSubjectChange: (next: 'uid' | 'anonymous') => void;
  onDepthChange: (next: string) => void;
};
export function PathSection({
  id,
  path,
  subject,
  depth,
  events,
  onPathChange,
  onSubjectChange,
  onDepthChange,
  readOnly,
  disabled,
}: PathSectionProps) {
  if (!path) return null;
  return (
    <>
      <div className="field-grid">
        <Field label="路径主体" htmlFor={`${id}-path-subject`}>
          <Select
            id={`${id}-path-subject`}
            disabled={readOnly || disabled}
            value={subject ?? 'uid'}
            options={[
              { value: 'uid', label: '原始 UID' },
              { value: 'anonymous', label: '匿名标识' },
            ]}
            onChange={(event) => onSubjectChange(event.target.value as 'uid' | 'anonymous')}
          />
        </Field>
        <Field label="路径深度（2 至 8）" htmlFor={`${id}-path-depth`}>
          <Input
            id={`${id}-path-depth`}
            type="number"
            min={2}
            max={8}
            step={1}
            required
            readOnly={readOnly}
            disabled={disabled}
            value={depth}
            onChange={(event) => onDepthChange(event.target.value)}
          />
        </Field>
      </div>
      {!readOnly && (
        <Checkbox
          checked={path.collapse_repeats === true}
          disabled={disabled}
          onChange={(event) => onPathChange({ ...path, collapse_repeats: event.target.checked })}
        >
          折叠连续重复事件（相邻相同事件合并为一个节点后再取深度）
        </Checkbox>
      )}
      {path.start_event ? (
        <div className="saved-analysis-editor-row">
          <PinControls
            id={id}
            pin={path.start_event}
            events={events}
            disabled={readOnly || disabled}
            label="起始事件 "
            onChange={(pin) =>
              onPathChange({ ...path, start_event: { ...path.start_event!, ...pin } })
            }
          />
          {!readOnly && (
            <Button
              disabled={disabled}
              onClick={() => onPathChange({ ...path, start_event: undefined })}
            >
              移除起始事件
            </Button>
          )}
        </div>
      ) : (
        !readOnly && (
          <Button
            disabled={disabled}
            onClick={() =>
              onPathChange({ ...path, start_event: { event_key: '', schema_version: 0 } })
            }
          >
            钉选起始事件
          </Button>
        )
      )}
      <p className="saved-analysis-editor-help">
        不钉选起始事件时，路径读取时间窗内主体的前 depth
        个事件，不固定任何事件版本；钉选后从匹配的最早起始事件开始取链路。
      </p>
    </>
  );
}
