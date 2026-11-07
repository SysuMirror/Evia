import {
  createContext,
  forwardRef,
  useContext,
  useId,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'link';
  loading?: boolean;
  size?: 'small' | 'medium' | 'large';
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'secondary',
    loading = false,
    size = 'medium',
    type = 'button',
    className = '',
    disabled,
    children,
    ...props
  },
  ref,
) {
  return (
    <button
      {...props}
      ref={ref}
      type={type}
      className={`evia-button evia-button-${variant} evia-button-${size} ${className}`}
      disabled={disabled || loading}
      aria-busy={loading || props['aria-busy'] || undefined}
    >
      {loading && <span className="evia-spinner" aria-hidden="true" />}
      {children}
    </button>
  );
});

interface FieldContextValue {
  id: string;
  describedBy?: string;
  invalid?: boolean;
  required?: boolean;
}

const FieldContext = createContext<FieldContextValue | null>(null);

export function Field({
  label,
  htmlFor,
  error,
  hint,
  required,
  children,
}: {
  label: ReactNode;
  htmlFor: string;
  error?: ReactNode;
  hint?: ReactNode;
  required?: boolean;
  children: ReactNode;
}) {
  const uniqueID = useId();
  const hintID = `${uniqueID}-hint`;
  const errorID = `${uniqueID}-error`;
  const describedBy =
    [hint ? hintID : '', error ? errorID : ''].filter(Boolean).join(' ') || undefined;
  return (
    <FieldContext.Provider value={{ id: htmlFor, describedBy, invalid: Boolean(error), required }}>
      <div className="evia-field">
        <label className="evia-label" htmlFor={htmlFor}>
          {label}
          {required && (
            <span className="evia-required" aria-hidden="true">
              {' '}
              *
            </span>
          )}
        </label>
        {children}
        {hint && (
          <div className="evia-field-hint" id={hintID}>
            {hint}
          </div>
        )}
        {error && (
          <div className="evia-field-error" id={errorID}>
            {error}
          </div>
        )}
      </div>
    </FieldContext.Provider>
  );
}

function useFieldProps<
  T extends {
    id?: string;
    required?: boolean;
    'aria-describedby'?: string;
    'aria-invalid'?: InputHTMLAttributes<HTMLInputElement>['aria-invalid'];
  },
>(props: T) {
  const field = useContext(FieldContext);
  return {
    id: props.id ?? field?.id,
    required: props.required ?? field?.required,
    'aria-describedby':
      [field?.describedBy, props['aria-describedby']].filter(Boolean).join(' ') || undefined,
    'aria-invalid': props['aria-invalid'] ?? (field?.invalid || undefined),
  };
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className = '', ...props }, ref) {
    const field = useFieldProps(props);
    return <input {...props} {...field} className={`evia-input ${className}`} ref={ref} />;
  },
);

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  TextareaHTMLAttributes<HTMLTextAreaElement>
>(function Textarea({ className = '', ...props }, ref) {
  const field = useFieldProps(props);
  return (
    <textarea {...props} {...field} className={`evia-input evia-textarea ${className}`} ref={ref} />
  );
});

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}
export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  options: readonly SelectOption[];
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className = '', options, ...props },
  ref,
) {
  const field = useFieldProps(props);
  return (
    <select {...props} {...field} className={`evia-input evia-select ${className}`} ref={ref}>
      {options.map((option) => (
        <option key={option.value} value={option.value} disabled={option.disabled}>
          {option.label}
        </option>
      ))}
    </select>
  );
});

export const Checkbox = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, 'type'>
>(function Checkbox({ children, className = '', ...props }, ref) {
  const field = useFieldProps(props);
  return (
    <label className={`evia-checkbox ${className}`}>
      <input {...props} {...field} ref={ref} type="checkbox" />
      <span>{children}</span>
    </label>
  );
});
