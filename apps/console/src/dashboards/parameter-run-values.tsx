import { useEffect, useId, useState } from 'react';
import type { ParameterDeclaration, ParameterValues } from '@evia/api-client';
import { Alert, Button, Field, Input, Modal, Select } from '@evia/design-system';
import { buildParameterValues, parameterValueText } from './parameter-model';

/**
 * Runtime value form for the dashboard-level parameters. The server remains the
 * authoritative validator; this mirrors the required/type checks so authors get
 * immediate feedback before a run is enqueued.
 */
export function DashboardParameterValues({
  declarations,
  initial,
  open,
  disabled,
  onClose,
  onApply,
  onPendingChange,
}: {
  declarations: readonly ParameterDeclaration[];
  initial: ParameterValues;
  open: boolean;
  disabled: boolean;
  onClose: () => void;
  onApply: (values: ParameterValues) => void;
  onPendingChange: (pending: boolean) => void;
}) {
  const id = useId();
  const [texts, setTexts] = useState<Record<string, string>>({});
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    const next: Record<string, string> = {};
    for (const decl of declarations) {
      const applied = initial[decl.parameter_id];
      next[decl.parameter_id] =
        applied !== undefined ? parameterValueText(applied) : parameterValueText(decl.default);
    }
    setTexts(next);
    setError('');
  }, [open, declarations, initial]);

  const baseline = JSON.stringify(
    declarations.map((decl) => [decl.parameter_id, initial[decl.parameter_id] ?? '']),
  );
  const changed =
    JSON.stringify(
      declarations.map((decl) => [decl.parameter_id, texts[decl.parameter_id] ?? '']),
    ) !== baseline;
  useEffect(() => onPendingChange(changed), [changed, onPendingChange]);

  function apply() {
    try {
      for (const decl of declarations) {
        const text = (texts[decl.parameter_id] ?? '').trim();
        if (text === '' && decl.required && decl.default === undefined)
          throw new Error(`必填参数 ${decl.label} 缺少运行值。`);
      }
      onApply(buildParameterValues(declarations, texts));
      setError('');
      onClose();
    } catch (failure) {
      setError((failure as Error).message);
    }
  }

  return (
    <Modal
      open={open}
      title="看板运行参数"
      onRequestClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>返回</Button>
          <Button variant="primary" type="submit" form={`${id}-form`} disabled={disabled}>
            应用参数
          </Button>
        </>
      }
    >
      <form
        id={`${id}-form`}
        className="section-stack"
        onSubmit={(event) => {
          event.preventDefault();
          apply();
        }}
      >
        <p className="field-help">
          参数值只作用于已绑定该参数的组件；留空时使用默认值，必填且无默认值的参数必须填写。
        </p>
        {error && <Alert tone="error" title={error} />}
        {declarations.map((decl) => (
          <Field
            key={decl.parameter_id}
            label={`${decl.label}（${decl.parameter_id}）${decl.required ? ' *' : ''}`}
            htmlFor={`${id}-${decl.parameter_id}`}
            hint={
              decl.operator === 'in'
                ? 'JSON 数组，例如 ["a","b"]'
                : decl.default !== undefined && decl.default !== null
                  ? `默认值：${parameterValueText(decl.default)}`
                  : undefined
            }
          >
            {decl.type === 'boolean' ? (
              <Select
                id={`${id}-${decl.parameter_id}`}
                disabled={disabled}
                value={texts[decl.parameter_id] ?? ''}
                options={[
                  { value: '', label: '使用默认值（若有）' },
                  { value: 'true', label: 'true' },
                  { value: 'false', label: 'false' },
                ]}
                onChange={(event) =>
                  setTexts((items) => ({ ...items, [decl.parameter_id]: event.target.value }))
                }
              />
            ) : (
              <Input
                id={`${id}-${decl.parameter_id}`}
                disabled={disabled}
                value={texts[decl.parameter_id] ?? ''}
                onChange={(event) =>
                  setTexts((items) => ({ ...items, [decl.parameter_id]: event.target.value }))
                }
              />
            )}
          </Field>
        ))}
      </form>
    </Modal>
  );
}
