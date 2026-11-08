import type { EventDefinition } from '@evia/api-client';
import { Field, Select } from '@evia/design-system';
import type { PublishedField, QueryField } from '../analysis-model';
import { savedFieldID, savedFieldLabel, type SavedPin } from '../saved-analysis-model';

export function FieldSelect({
  id,
  field,
  onChange,
  label,
  fields,
  optional = false,
  disabled,
}: {
  id: string;
  field: QueryField | undefined;
  onChange: (next: QueryField | undefined) => void;
  label: string;
  fields: PublishedField[];
  optional?: boolean;
  disabled: boolean;
}) {
  const current = field ? savedFieldID(field) : '';
  const choices = [...fields];
  if (field && !choices.some((item) => item.id === current))
    choices.unshift({
      id: current,
      label: savedFieldLabel(field),
      type: 'unknown',
      reference: field,
    });
  const fieldID = `${id}-field-${label.replace(/\s+/g, '-')}`;
  return (
    <Field label={label} htmlFor={fieldID}>
      <Select
        id={fieldID}
        disabled={disabled}
        value={current}
        options={[
          { value: '', label: optional ? '身份主体' : '请选择字段', disabled: !optional },
          ...choices.map((item) => ({ value: item.id, label: item.label })),
        ]}
        onChange={(event) =>
          onChange(
            event.target.value
              ? choices.find((item) => item.id === event.target.value)?.reference
              : undefined,
          )
        }
      />
    </Field>
  );
}

export function PinControls({
  id,
  pin,
  onChange,
  label,
  events,
  disabled,
}: {
  id: string;
  pin: SavedPin;
  onChange: (next: SavedPin) => void;
  label: string;
  events: EventDefinition[];
  disabled: boolean;
}) {
  const known = events.find((event) => event.event_key === pin.event_key);
  const eventOptions = events.map((event) => ({
    value: event.event_key,
    label: `${event.display_name} (${event.event_key})`,
  }));
  if (pin.event_key && !eventOptions.some((item) => item.value === pin.event_key))
    eventOptions.unshift({ value: pin.event_key, label: pin.event_key });
  const versionOptions =
    known?.published_versions.map((version) => ({
      value: String(version),
      label: `v${version}`,
    })) ?? [];
  if (
    pin.schema_version > 0 &&
    !versionOptions.some((item) => item.value === String(pin.schema_version))
  )
    versionOptions.unshift({ value: String(pin.schema_version), label: `v${pin.schema_version}` });
  const pinID = `${id}-pin-${label.replace(/\s+/g, '-')}`;
  return (
    <>
      <Field label={`${label}事件`} htmlFor={`${pinID}-event`}>
        <Select
          id={`${pinID}-event`}
          disabled={disabled}
          value={pin.event_key}
          options={[{ value: '', label: '请选择已发布事件', disabled: true }, ...eventOptions]}
          onChange={(event) => onChange({ event_key: event.target.value, schema_version: 0 })}
        />
      </Field>
      <Field label={`${label}版本`} htmlFor={`${pinID}-version`}>
        <Select
          id={`${pinID}-version`}
          disabled={disabled || !pin.event_key}
          value={pin.schema_version > 0 ? String(pin.schema_version) : ''}
          options={[{ value: '', label: '请选择版本', disabled: true }, ...versionOptions]}
          onChange={(event) => onChange({ ...pin, schema_version: Number(event.target.value) })}
        />
      </Field>
    </>
  );
}
