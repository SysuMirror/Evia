import type { RefObject } from 'react';
import { Field, Textarea } from '@evia/design-system';
import type { SectionControlProps } from './types';

type AdvancedSectionProps = SectionControlProps & {
  text: string;
  error: string;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  onChange: (next: string) => void;
};
export function AdvancedSection({
  id,
  text,
  error,
  inputRef,
  onChange,
  readOnly,
  disabled,
}: AdvancedSectionProps) {
  return (
    <Field
      label="完整分析定义"
      htmlFor={`${id}-json`}
      hint="修改后应用配置，已有高级筛选和步骤条件会完整保留。"
    >
      <Textarea
        ref={inputRef}
        id={`${id}-json`}
        className="saved-analysis-editor-json"
        rows={18}
        spellCheck={false}
        readOnly={readOnly}
        disabled={disabled}
        value={text}
        aria-invalid={!!error}
        onChange={(event) => onChange(event.target.value)}
      />
    </Field>
  );
}
