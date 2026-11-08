import type { EventDefinition } from '@evia/api-client';
import { Button } from '@evia/design-system';
import type { SavedPin } from '../saved-analysis-model';
import { PinControls } from './selection-controls';
import type { SectionControlProps } from './types';

type EventsSectionProps = SectionControlProps & {
  pins: SavedPin[];
  events: EventDefinition[];
  onChange: (next: SavedPin[]) => void;
};
export function EventsSection({
  id,
  pins,
  events,
  onChange,
  readOnly,
  disabled,
}: EventsSectionProps) {
  return (
    <div className="saved-analysis-editor-rows section-stack">
      {pins.map((pin, index) => (
        <div className="saved-analysis-editor-row" key={index}>
          <PinControls
            id={id}
            pin={pin}
            events={events}
            disabled={readOnly || disabled}
            label={`第 ${index + 1} 个`}
            onChange={(next) =>
              onChange(pins.map((item, itemIndex) => (itemIndex === index ? next : item)))
            }
          />
          {!readOnly && (
            <Button
              disabled={disabled}
              onClick={() => onChange(pins.filter((_, itemIndex) => itemIndex !== index))}
            >
              移除版本 {index + 1}
            </Button>
          )}
        </div>
      ))}
      {!readOnly && (
        <Button
          disabled={disabled || pins.length >= 20}
          onClick={() => onChange([...pins, { event_key: '', schema_version: 0 }])}
        >
          添加事件版本
        </Button>
      )}
      <p className="saved-analysis-editor-help">
        同一事件固定一个版本，属性引用须属于所选版本。调整版本时，先更新相关引用，或在高级 JSON
        中一起修改。
      </p>
    </div>
  );
}
