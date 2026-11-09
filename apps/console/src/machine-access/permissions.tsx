import { Checkbox } from '@evia/design-system';
import type { MachinePermission } from '@evia/api-client';
import { permissionGroups } from './model';

export function MachinePermissions({
  value,
  onChange,
  disabled = false,
}: {
  value: MachinePermission[];
  onChange?: (value: MachinePermission[]) => void;
  disabled?: boolean;
}) {
  return (
    <div className="machine-permissions">
      {permissionGroups.map((group) => {
        const selected = group.items.filter((item) => value.includes(item.value));
        if (!onChange && !selected.length) return null;
        return (
          <fieldset key={group.name} className="machine-permission-group" disabled={disabled}>
            <legend>{group.name}</legend>
            {onChange ? (
              <div className="machine-permission-options">
                {group.items.map((item) => (
                  <Checkbox
                    key={item.value}
                    checked={value.includes(item.value)}
                    onChange={(event) =>
                      onChange(
                        event.target.checked
                          ? [...value, item.value]
                          : value.filter((permission) => permission !== item.value),
                      )
                    }
                  >
                    {item.label}
                  </Checkbox>
                ))}
              </div>
            ) : (
              <p>{selected.map((item) => item.label).join('，')}</p>
            )}
            {group.help && <p className="evia-field-hint">{group.help}</p>}
          </fieldset>
        );
      })}
    </div>
  );
}
