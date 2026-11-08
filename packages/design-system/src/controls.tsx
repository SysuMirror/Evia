import {
  createContext,
  forwardRef,
  useContext,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
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
    onFocus,
    onBlur,
    children,
    ...props
  },
  ref,
) {
  const buttonRef = useRef<HTMLButtonElement>(null);
  const focused = useRef(false);
  const previousLoading = useRef(false);
  const restoreFocus = useRef(false);
  useImperativeHandle(ref, () => buttonRef.current!, []);
  useLayoutEffect(() => {
    const button = buttonRef.current;
    if (!button) return;
    const beganLoading = loading && !previousLoading.current;
    const endedLoading = !loading && previousLoading.current;
    previousLoading.current = loading;
    if (beganLoading) restoreFocus.current = focused.current || document.activeElement === button;
    if (!loading && !endedLoading) return;
    const relinquish = () => {
      restoreFocus.current = false;
      focused.current = false;
    };
    const focusMoved = (event: FocusEvent) => {
      if (event.target !== button && event.target !== document.body) relinquish();
    };
    const pointerMoved = (event: PointerEvent) => {
      if (event.target !== button) relinquish();
    };
    const tabPressed = (event: KeyboardEvent) => {
      if (event.key === 'Tab') relinquish();
    };
    document.addEventListener('focusin', focusMoved);
    document.addEventListener('pointerdown', pointerMoved, true);
    document.addEventListener('keydown', tabPressed, true);
    window.addEventListener('blur', relinquish);
    const removeListeners = () => {
      document.removeEventListener('focusin', focusMoved);
      document.removeEventListener('pointerdown', pointerMoved, true);
      document.removeEventListener('keydown', tabPressed, true);
      window.removeEventListener('blur', relinquish);
    };
    const frame = endedLoading
      ? requestAnimationFrame(() => {
          if (
            restoreFocus.current &&
            document.activeElement === document.body &&
            !button.matches(':disabled') &&
            button.getClientRects().length > 0
          )
            button.focus({ preventScroll: true });
          restoreFocus.current = false;
          focused.current = document.activeElement === button;
          removeListeners();
        })
      : undefined;
    return () => {
      if (frame !== undefined) cancelAnimationFrame(frame);
      removeListeners();
    };
  }, [loading, disabled]);
  return (
    <button
      {...props}
      ref={buttonRef}
      type={type}
      className={`evia-button evia-button-${variant} evia-button-${size} ${className}`}
      disabled={disabled || loading}
      aria-busy={loading || props['aria-busy'] || undefined}
      onFocus={(event) => {
        focused.current = true;
        onFocus?.(event);
      }}
      onBlur={(event) => {
        if (!loading || event.relatedTarget) focused.current = false;
        onBlur?.(event);
      }}
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
  className = '',
}: {
  label: ReactNode;
  htmlFor: string;
  error?: ReactNode;
  hint?: ReactNode;
  required?: boolean;
  children: ReactNode;
  className?: string;
}) {
  const uniqueID = useId();
  const hintID = `${uniqueID}-hint`;
  const errorID = `${uniqueID}-error`;
  const describedBy =
    [hint ? hintID : '', error ? errorID : ''].filter(Boolean).join(' ') || undefined;
  return (
    <FieldContext.Provider value={{ id: htmlFor, describedBy, invalid: Boolean(error), required }}>
      <div className={`evia-field ${className}`}>
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
        <div className="evia-field-error" id={errorID} aria-live="polite" aria-atomic="true">
          {error}
        </div>
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
