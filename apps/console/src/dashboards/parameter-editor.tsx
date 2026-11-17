import { useId, useState } from 'react';
import type { DashboardDefinition, ParameterDeclaration } from '@evia/api-client';
import { Button, Checkbox, Field, Input, Select } from '@evia/design-system';
import {
  MAX_PARAMETERS,
  parameterOperatorOptions,
  parameterTypeOptions,
  parameterValueText,
  parseParameterValue,
} from './parameter-model';

type Change = (definition: DashboardDefinition) => void;

function emptyDeclaration(index: number): ParameterDeclaration {
  return {
    parameter_id: `parameter_${index + 1}`,
    label: `参数 ${index + 1}`,
    type: 'string',
    operator: 'eq',
    required: false,
  };
}

export function DashboardParameterEditor({
  value,
  frozen,
  onChange,
}: {
  value: DashboardDefinition;
  frozen: boolean;
  onChange: Change;
}) {
  const id = useId();
  const parameters = value.parameters ?? [];
  // Local text for the default input keeps typing responsive; only parseable
  // values are committed into the definition that gets saved.
  const [defaultText, setDefaultText] = useState<Record<string, string>>({});
  const [defaultError, setDefaultError] = useState<Record<string, string>>({});

  function patch(next: ParameterDeclaration[]) {
    onChange({ ...value, parameters: next.length ? next : undefined });
  }
  function update(parameterID: string, partial: Partial<ParameterDeclaration>) {
    patch(
      parameters.map((decl) =>
        decl.parameter_id === parameterID ? { ...decl, ...partial } : decl,
      ),
    );
  }

  return (
    <fieldset disabled={frozen} className="section-stack dashboard-parameter-editor">
      <legend>
        看板参数（{parameters.length} / {MAX_PARAMETERS}）
      </legend>
      <p className="field-help">
        参数只声明运行时值与类型，不保存字段名；每个组件在编辑弹窗中自行把参数绑定到自己的固定事件字段。
      </p>
      {parameters.map((decl, index) => (
        <div key={`${decl.parameter_id}:${index}`} className="dashboard-parameter-row">
          <Field label="参数标识" htmlFor={`${id}-${index}-parameter-id`}>
            <Input
              id={`${id}-${index}-parameter-id`}
              value={decl.parameter_id}
              maxLength={64}
              required
              onChange={(event) => update(decl.parameter_id, { parameter_id: event.target.value })}
            />
          </Field>
          <Field label="显示标签" htmlFor={`${id}-${index}-label`}>
            <Input
              id={`${id}-${index}-label`}
              value={decl.label}
              maxLength={128}
              required
              onChange={(event) => update(decl.parameter_id, { label: event.target.value })}
            />
          </Field>
          <Field label="类型" htmlFor={`${id}-${index}-type`}>
            <Select
              id={`${id}-${index}-type`}
              value={decl.type}
              options={parameterTypeOptions.map((option) => ({
                value: option.value,
                label: option.label,
              }))}
              onChange={(event) => {
                const type = event.target.value as ParameterDeclaration['type'];
                const operator = parameterOperatorOptions(type)[0]?.value ?? 'eq';
                setDefaultText((items) => ({ ...items, [decl.parameter_id]: '' }));
                update(decl.parameter_id, {
                  type,
                  operator: operator as ParameterDeclaration['operator'],
                  default: undefined,
                });
              }}
            />
          </Field>
          <Field label="运算" htmlFor={`${id}-${index}-operator`}>
            <Select
              id={`${id}-${index}-operator`}
              value={decl.operator}
              options={parameterOperatorOptions(decl.type)}
              onChange={(event) => {
                const operator = event.target.value as ParameterDeclaration['operator'];
                // Array defaults are only valid for the in operator.
                if (operator !== 'in' && Array.isArray(decl.default)) {
                  setDefaultText((items) => ({ ...items, [decl.parameter_id]: '' }));
                  update(decl.parameter_id, { operator, default: undefined });
                  return;
                }
                update(decl.parameter_id, { operator, default: decl.default });
              }}
            />
          </Field>
          <Checkbox
            checked={decl.required}
            onChange={(event) => update(decl.parameter_id, { required: event.target.checked })}
          >
            必填
          </Checkbox>
          <Field
            label="默认值（可选）"
            htmlFor={`${id}-${index}-default`}
            hint={
              decl.operator === 'in'
                ? 'JSON 数组，例如 ["a","b"]'
                : decl.type === 'boolean'
                  ? 'true 或 false'
                  : undefined
            }
          >
            {decl.type === 'boolean' ? (
              <Select
                id={`${id}-${index}-default`}
                value={decl.default === undefined ? '' : String(decl.default)}
                options={[
                  { value: '', label: '无默认值' },
                  { value: 'true', label: 'true' },
                  { value: 'false', label: 'false' },
                ]}
                onChange={(event) =>
                  update(decl.parameter_id, {
                    default: event.target.value === '' ? undefined : event.target.value === 'true',
                  })
                }
              />
            ) : (
              <Input
                id={`${id}-${index}-default`}
                value={defaultText[decl.parameter_id] ?? parameterValueText(decl.default)}
                onChange={(event) => {
                  const text = event.target.value;
                  setDefaultText((items) => ({ ...items, [decl.parameter_id]: text }));
                  if (text.trim() === '') {
                    setDefaultError((items) => ({ ...items, [decl.parameter_id]: '' }));
                    update(decl.parameter_id, { default: undefined });
                    return;
                  }
                  try {
                    update(decl.parameter_id, { default: parseParameterValue(decl, text) });
                    setDefaultError((items) => ({ ...items, [decl.parameter_id]: '' }));
                  } catch (failure) {
                    setDefaultError((items) => ({
                      ...items,
                      [decl.parameter_id]: (failure as Error).message,
                    }));
                  }
                }}
              />
            )}
          </Field>
          {defaultError[decl.parameter_id] && (
            <p className="field-help" role="alert">
              {defaultError[decl.parameter_id]}
            </p>
          )}
          <Button
            variant="danger"
            onClick={() =>
              patch(parameters.filter((item) => item.parameter_id !== decl.parameter_id))
            }
          >
            移除参数
          </Button>
        </div>
      ))}
      <Button
        disabled={parameters.length >= MAX_PARAMETERS}
        onClick={() => patch([...parameters, emptyDeclaration(parameters.length)])}
      >
        添加参数
      </Button>
    </fieldset>
  );
}
