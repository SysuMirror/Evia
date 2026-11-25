import {
  createContext,
  forwardRef,
  useContext,
  useEffect,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react';
import { createPortal } from 'react-dom';
import { DateTimeInput } from './datetime-input';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'link';
  loading?: boolean;
  size?: 'small' | 'medium' | 'large';
  startIcon?: ReactNode;
  endIcon?: ReactNode;
  iconOnly?: boolean;
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
    startIcon,
    endIcon,
    iconOnly = false,
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
      className={`evia-button evia-button-${variant} evia-button-${size}${iconOnly ? ' evia-button-icon-only' : ''} ${className}`}
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
      {loading && <span className="evia-spinner evia-button-spinner" aria-hidden="true" />}
      <span className={`evia-button-content${loading ? ' evia-button-content-loading' : ''}`}>
        {startIcon && (
          <span className="evia-button-icon" aria-hidden="true">
            {startIcon}
          </span>
        )}
        {children}
        {endIcon && (
          <span className="evia-button-icon" aria-hidden="true">
            {endIcon}
          </span>
        )}
      </span>
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
  help,
  required,
  children,
  className = '',
}: {
  label: ReactNode;
  htmlFor: string;
  error?: ReactNode;
  hint?: ReactNode;
  help?: ReactNode;
  required?: boolean;
  children: ReactNode;
  className?: string;
}) {
  const uniqueID = useId();
  const hintID = `${uniqueID}-hint`;
  const errorID = `${uniqueID}-error`;
  const helpContent = help ?? hint;
  const describedBy =
    [helpContent ? hintID : '', error ? errorID : ''].filter(Boolean).join(' ') || undefined;
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
        {helpContent && (
          <div className="evia-field-hint" id={hintID}>
            {helpContent}
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
    if (props.type === 'datetime-local') {
      return <DateTimeInput {...props} {...field} className={className} ref={ref} />;
    }
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

function findEnabledIndex(options: readonly SelectOption[], start: number, step: 1 | -1): number {
  if (options.length === 0) return -1;
  let index = start;
  while (index >= 0 && index < options.length) {
    if (!options[index]?.disabled) return index;
    index += step;
  }
  return -1;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  {
    className = '',
    options,
    value,
    defaultValue,
    disabled,
    onChange,
    onKeyDown,
    onMouseDown,
    onBlur,
    ...props
  },
  ref,
) {
  const field = useFieldProps(props);
  const selectRef = useRef<HTMLSelectElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const listID = useId();
  useImperativeHandle(ref, () => selectRef.current!, []);

  const [uncontrolledValue, setUncontrolledValue] = useState<string>(() =>
    defaultValue !== undefined ? String(defaultValue) : (options[0]?.value ?? ''),
  );
  const [open, setOpen] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const [menuStyle, setMenuStyle] = useState<CSSProperties>({});

  const currentValue = value !== undefined ? String(value) : uncontrolledValue;
  const selectedIndex = options.findIndex((option) => option.value === currentValue);
  const selectedOption = selectedIndex >= 0 ? options[selectedIndex] : options[0];

  const commitValue = (nextValue: string) => {
    const select = selectRef.current;
    if (!select || disabled) return;
    if (value === undefined) setUncontrolledValue(nextValue);
    if (select.value !== nextValue) {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')?.set;
      setter?.call(select, nextValue);
      select.dispatchEvent(new Event('change', { bubbles: true }));
    }
    setOpen(false);
  };

  useLayoutEffect(() => {
    if (!open || !wrapRef.current) return;
    const updatePosition = () => {
      const wrap = wrapRef.current;
      if (!wrap) return;
      const rect = wrap.getBoundingClientRect();
      const viewportW = window.innerWidth;
      const viewportH = window.innerHeight;
      const spaceBelow = viewportH - rect.bottom - 12;
      const spaceAbove = rect.top - 12;
      const openUpward = spaceBelow < 200 && spaceAbove > spaceBelow;
      const maxHeight = Math.max(120, Math.min(280, openUpward ? spaceAbove - 6 : spaceBelow - 6));
      const minWidth = Math.min(viewportW - 16, Math.max(rect.width, 160));
      const maxWidth = Math.min(viewportW - 16, Math.max(rect.width, 360));
      const left = Math.min(Math.max(8, rect.left), Math.max(8, viewportW - minWidth - 8));
      setMenuStyle(
        openUpward
          ? {
              position: 'fixed',
              left,
              bottom: viewportH - rect.top + 6,
              minWidth,
              maxWidth,
              maxHeight,
            }
          : {
              position: 'fixed',
              left,
              top: rect.bottom + 6,
              minWidth,
              maxWidth,
              maxHeight,
            },
      );
    };
    updatePosition();
    const handleScroll = (event: Event) => {
      if (menuRef.current && event.target instanceof Node && menuRef.current.contains(event.target))
        return;
      updatePosition();
    };
    window.addEventListener('resize', updatePosition);
    window.addEventListener('scroll', handleScroll, true);
    return () => {
      window.removeEventListener('resize', updatePosition);
      window.removeEventListener('scroll', handleScroll, true);
    };
  }, [open, options.length]);

  useEffect(() => {
    if (!open) return;
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (!target) return;
      if (wrapRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener('pointerdown', handlePointerDown, true);
    return () => document.removeEventListener('pointerdown', handlePointerDown, true);
  }, [open]);

  useEffect(() => {
    if (!open || highlightedIndex < 0) return;
    const optionEl = menuRef.current?.querySelector<HTMLElement>(
      `[data-option-index="${highlightedIndex}"]`,
    );
    optionEl?.scrollIntoView({ block: 'nearest' });
  }, [open, highlightedIndex]);

  useEffect(() => {
    if (disabled && open) setOpen(false);
  }, [disabled, open]);

  const handleMouseDown = (event: ReactMouseEvent<HTMLSelectElement>) => {
    onMouseDown?.(event);
    if (event.defaultPrevented || disabled || event.button !== 0) return;
    event.preventDefault();
    selectRef.current?.focus();
    setOpen((wasOpen) => {
      if (wasOpen) return false;
      setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : findEnabledIndex(options, 0, 1));
      return true;
    });
  };

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLSelectElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented || disabled) return;

    if (event.key === 'ArrowDown') {
      event.preventDefault();
      if (event.altKey) {
        setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : findEnabledIndex(options, 0, 1));
        setOpen(true);
        return;
      }
      const base = open ? highlightedIndex : selectedIndex;
      const next = findEnabledIndex(options, base + 1, 1);
      if (next >= 0) setHighlightedIndex(next);
      else if (!open && selectedIndex >= 0) setHighlightedIndex(selectedIndex);
      setOpen(true);
      return;
    }

    if (event.key === 'ArrowUp') {
      event.preventDefault();
      if (event.altKey) {
        setOpen(false);
        return;
      }
      const base = open ? highlightedIndex : selectedIndex;
      const prev = findEnabledIndex(options, base < 0 ? options.length - 1 : base - 1, -1);
      if (prev >= 0) setHighlightedIndex(prev);
      else if (!open && selectedIndex >= 0) setHighlightedIndex(selectedIndex);
      setOpen(true);
      return;
    }

    if (event.key === 'Home' && open) {
      event.preventDefault();
      const first = findEnabledIndex(options, 0, 1);
      if (first >= 0) setHighlightedIndex(first);
      return;
    }

    if (event.key === 'End' && open) {
      event.preventDefault();
      const last = findEnabledIndex(options, options.length - 1, -1);
      if (last >= 0) setHighlightedIndex(last);
      return;
    }

    if (event.key === ' ') {
      event.preventDefault();
      if (!open) {
        setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : findEnabledIndex(options, 0, 1));
        setOpen(true);
      } else {
        const option = options[highlightedIndex];
        if (option && !option.disabled) commitValue(option.value);
      }
      return;
    }

    if (event.key === 'Enter' && open) {
      event.preventDefault();
      const option = options[highlightedIndex];
      if (option && !option.disabled) commitValue(option.value);
      else setOpen(false);
      return;
    }

    if (event.key === 'Escape' && open) {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      return;
    }

    if (event.key === 'Tab' && open) {
      setOpen(false);
    }
  };

  const portalTarget =
    typeof document !== 'undefined' ? (wrapRef.current?.closest('dialog') ?? document.body) : null;

  return (
    <div
      ref={wrapRef}
      className={`evia-input evia-select ${className}`}
      data-open={open || undefined}
      data-disabled={disabled || undefined}
      data-invalid={field['aria-invalid'] ? 'true' : undefined}
    >
      <span className="evia-select-value" aria-hidden="true">
        {selectedOption?.label ?? ''}
      </span>
      <span className="evia-select-chevron" aria-hidden="true">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
          <path
            d="m4 6 4 4 4-4"
            stroke="currentColor"
            strokeWidth="1.65"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </span>
      <select
        {...props}
        {...field}
        ref={selectRef}
        className="evia-select-native"
        value={value}
        defaultValue={defaultValue}
        disabled={disabled}
        aria-expanded={open}
        aria-controls={open ? listID : undefined}
        onMouseDown={handleMouseDown}
        onKeyDown={handleKeyDown}
        onChange={(event) => {
          if (value === undefined) setUncontrolledValue(event.target.value);
          onChange?.(event);
        }}
        onBlur={(event) => {
          setOpen(false);
          onBlur?.(event);
        }}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value} disabled={option.disabled}>
            {option.label}
          </option>
        ))}
      </select>
      {open &&
        portalTarget &&
        createPortal(
          <div
            ref={menuRef}
            id={listID}
            role="listbox"
            className="evia-select-popover"
            style={menuStyle}
            onMouseDown={(event) => event.preventDefault()}
          >
            {options.map((option, index) => {
              const isSelected = option.value === selectedOption?.value && selectedIndex === index;
              const isHighlighted = index === highlightedIndex;
              return (
                <button
                  key={option.value}
                  type="button"
                  role="option"
                  tabIndex={-1}
                  disabled={option.disabled}
                  aria-selected={isSelected}
                  data-option-index={index}
                  data-highlighted={isHighlighted || undefined}
                  className="evia-select-option"
                  onMouseEnter={() => {
                    if (!option.disabled) setHighlightedIndex(index);
                  }}
                  onClick={() => {
                    if (!option.disabled) commitValue(option.value);
                  }}
                >
                  <span className="evia-select-option-label">{option.label}</span>
                  {isSelected && (
                    <span className="evia-select-check" aria-hidden="true">
                      <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                        <path
                          d="M3.5 8.5 6.5 11.5 12.5 5"
                          stroke="currentColor"
                          strokeWidth="1.8"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                        />
                      </svg>
                    </span>
                  )}
                </button>
              );
            })}
          </div>,
          portalTarget,
        )}
    </div>
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
