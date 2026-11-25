import {
  forwardRef,
  useEffect,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from 'react';
import { createPortal } from 'react-dom';

interface ParsedDateTime {
  year: number;
  month: number; // 1-12
  day: number; // 1-31
  hour: number; // 0-23
  minute: number; // 0-59
}

const WEEKDAYS = ['一', '二', '三', '四', '五', '六', '日'] as const;
const HOURS = Array.from({ length: 24 }, (_, i) => i);
const MINUTES = Array.from({ length: 60 }, (_, i) => i);

function pad2(value: number): string {
  return String(value).padStart(2, '0');
}

function parseDateTime(raw: string): ParsedDateTime | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(raw.trim());
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  if (month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59) return null;
  return { year, month, day, hour, minute };
}

function formatDateTime(value: ParsedDateTime): string {
  return `${String(value.year).padStart(4, '0')}-${pad2(value.month)}-${pad2(value.day)}T${pad2(value.hour)}:${pad2(value.minute)}`;
}

function formatDisplay(raw: string): string {
  const parsed = parseDateTime(raw);
  if (!parsed) return '';
  return `${String(parsed.year).padStart(4, '0')}-${pad2(parsed.month)}-${pad2(parsed.day)} ${pad2(parsed.hour)}:${pad2(parsed.minute)}`;
}

function daysInMonth(year: number, month: number): number {
  return new Date(year, month, 0).getDate();
}

function buildCalendarCells(viewYear: number, viewMonth: number) {
  const firstDow = (new Date(viewYear, viewMonth - 1, 1).getDay() + 6) % 7; // Monday = 0
  const totalDays = daysInMonth(viewYear, viewMonth);
  const prevMonth = viewMonth === 1 ? 12 : viewMonth - 1;
  const prevYear = viewMonth === 1 ? viewYear - 1 : viewYear;
  const prevDays = daysInMonth(prevYear, prevMonth);
  const nextMonth = viewMonth === 12 ? 1 : viewMonth + 1;
  const nextYear = viewMonth === 12 ? viewYear + 1 : viewYear;

  const cells: { year: number; month: number; day: number; outside: boolean }[] = [];
  for (let i = firstDow - 1; i >= 0; i--) {
    cells.push({ year: prevYear, month: prevMonth, day: prevDays - i, outside: true });
  }
  for (let d = 1; d <= totalDays; d++) {
    cells.push({ year: viewYear, month: viewMonth, day: d, outside: false });
  }
  let trailing = 1;
  while (cells.length < 42) {
    cells.push({ year: nextYear, month: nextMonth, day: trailing++, outside: true });
  }
  return cells;
}

function dateKey(year: number, month: number, day: number): string {
  return `${String(year).padStart(4, '0')}-${pad2(month)}-${pad2(day)}`;
}

export const DateTimeInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function DateTimeInput(
    {
      className = '',
      value,
      defaultValue,
      disabled,
      readOnly,
      required,
      min,
      max,
      onChange,
      onMouseDown,
      onKeyDown,
      onBlur,
      ...props
    },
    ref,
  ) {
    const inputRef = useRef<HTMLInputElement>(null);
    const wrapRef = useRef<HTMLDivElement>(null);
    const popoverRef = useRef<HTMLDivElement>(null);
    const hourListRef = useRef<HTMLDivElement>(null);
    const minuteListRef = useRef<HTMLDivElement>(null);
    const popoverID = useId();
    useImperativeHandle(ref, () => inputRef.current!, []);

    const [uncontrolledValue, setUncontrolledValue] = useState<string>(() =>
      defaultValue !== undefined ? String(defaultValue) : '',
    );
    const [open, setOpen] = useState(false);
    const [popoverStyle, setPopoverStyle] = useState<CSSProperties>({});

    const currentValue = value !== undefined ? String(value) : uncontrolledValue;
    const parsed = useMemo(() => parseDateTime(currentValue), [currentValue]);

    const today = useMemo(() => {
      const now = new Date();
      return { year: now.getFullYear(), month: now.getMonth() + 1, day: now.getDate() };
    }, []);

    const [viewYear, setViewYear] = useState<number>(() => parsed?.year ?? today.year);
    const [viewMonth, setViewMonth] = useState<number>(() => parsed?.month ?? today.month);

    const minDateKey = typeof min === 'string' && min.length >= 10 ? min.slice(0, 10) : '';
    const maxDateKey = typeof max === 'string' && max.length >= 10 ? max.slice(0, 10) : '';

    const cells = useMemo(() => buildCalendarCells(viewYear, viewMonth), [viewYear, viewMonth]);

    const commitValue = (nextValue: string) => {
      const input = inputRef.current;
      if (!input || disabled || readOnly) return;
      if (value === undefined) setUncontrolledValue(nextValue);
      if (input.value !== nextValue) {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
        setter?.call(input, nextValue);
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.dispatchEvent(new Event('change', { bubbles: true }));
      }
    };

    const openPicker = () => {
      if (disabled || readOnly) return;
      const base = parsed ?? {
        year: today.year,
        month: today.month,
        day: today.day,
        hour: 0,
        minute: 0,
      };
      setViewYear(base.year);
      setViewMonth(base.month);
      setOpen(true);
    };

    useLayoutEffect(() => {
      if (!open || !wrapRef.current) return;
      const updatePosition = () => {
        const wrap = wrapRef.current;
        if (!wrap) return;
        const rect = wrap.getBoundingClientRect();
        const viewportW = window.innerWidth;
        const viewportH = window.innerHeight;
        const panelW = Math.min(372, viewportW - 16);
        const panelH = 336;
        const spaceBelow = viewportH - rect.bottom - 12;
        const spaceAbove = rect.top - 12;
        const openUpward = spaceBelow < panelH && spaceAbove > spaceBelow;
        const left = Math.min(Math.max(8, rect.left), Math.max(8, viewportW - panelW - 8));
        setPopoverStyle(
          openUpward
            ? { position: 'fixed', left, bottom: viewportH - rect.top + 6, width: panelW }
            : { position: 'fixed', left, top: rect.bottom + 6, width: panelW },
        );
      };
      updatePosition();
      const handleScroll = (event: Event) => {
        if (
          popoverRef.current &&
          event.target instanceof Node &&
          popoverRef.current.contains(event.target)
        )
          return;
        updatePosition();
      };
      window.addEventListener('resize', updatePosition);
      window.addEventListener('scroll', handleScroll, true);
      return () => {
        window.removeEventListener('resize', updatePosition);
        window.removeEventListener('scroll', handleScroll, true);
      };
    }, [open]);

    useEffect(() => {
      if (!open) return;
      for (const list of [hourListRef.current, minuteListRef.current]) {
        if (!list) continue;
        const active = list.querySelector<HTMLElement>('[aria-selected="true"]');
        if (active) {
          list.scrollTop = Math.max(
            0,
            active.offsetTop - list.offsetTop - list.clientHeight / 2 + active.clientHeight / 2,
          );
        }
      }
    }, [open]);

    useEffect(() => {
      if (!open) return;
      const handlePointerDown = (event: PointerEvent) => {
        const target = event.target as Node | null;
        if (!target) return;
        if (wrapRef.current?.contains(target) || popoverRef.current?.contains(target)) return;
        setOpen(false);
      };
      const handleGlobalKeyDown = (event: KeyboardEvent) => {
        if (event.key === 'Escape') {
          event.preventDefault();
          event.stopPropagation();
          setOpen(false);
          inputRef.current?.focus();
        }
      };
      document.addEventListener('pointerdown', handlePointerDown, true);
      document.addEventListener('keydown', handleGlobalKeyDown, true);
      return () => {
        document.removeEventListener('pointerdown', handlePointerDown, true);
        document.removeEventListener('keydown', handleGlobalKeyDown, true);
      };
    }, [open]);

    useEffect(() => {
      if ((disabled || readOnly) && open) setOpen(false);
    }, [disabled, readOnly, open]);

    const handleMouseDown = (event: ReactMouseEvent<HTMLInputElement>) => {
      onMouseDown?.(event);
      if (event.defaultPrevented || disabled || readOnly || event.button !== 0) return;
      event.preventDefault();
      inputRef.current?.focus();
      if (open) setOpen(false);
      else openPicker();
    };

    const handleKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
      onKeyDown?.(event);
      if (event.defaultPrevented || disabled || readOnly) return;
      if (!open && (event.key === 'ArrowDown' || event.key === ' ' || event.key === 'Enter')) {
        event.preventDefault();
        openPicker();
        return;
      }
      if (open && event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        return;
      }
      if (open && event.key === 'Tab') {
        setOpen(false);
      }
    };

    const stepMonth = (delta: -1 | 1) => {
      const next = viewMonth + delta;
      if (next < 1) {
        setViewYear((y) => y - 1);
        setViewMonth(12);
      } else if (next > 12) {
        setViewYear((y) => y + 1);
        setViewMonth(1);
      } else {
        setViewMonth(next);
      }
    };

    const pickDay = (year: number, month: number, day: number) => {
      const next: ParsedDateTime = {
        year,
        month,
        day,
        hour: parsed?.hour ?? 0,
        minute: parsed?.minute ?? 0,
      };
      setViewYear(year);
      setViewMonth(month);
      commitValue(formatDateTime(next));
    };

    const pickHour = (hour: number) => {
      const base = parsed ?? {
        year: today.year,
        month: today.month,
        day: today.day,
        hour: 0,
        minute: 0,
      };
      commitValue(formatDateTime({ ...base, hour }));
    };

    const pickMinute = (minute: number) => {
      const base = parsed ?? {
        year: today.year,
        month: today.month,
        day: today.day,
        hour: 0,
        minute: 0,
      };
      commitValue(formatDateTime({ ...base, minute }));
    };

    const pickNow = () => {
      const now = new Date();
      const next: ParsedDateTime = {
        year: now.getFullYear(),
        month: now.getMonth() + 1,
        day: now.getDate(),
        hour: now.getHours(),
        minute: now.getMinutes(),
      };
      setViewYear(next.year);
      setViewMonth(next.month);
      commitValue(formatDateTime(next));
    };

    const portalTarget =
      typeof document !== 'undefined'
        ? (wrapRef.current?.closest('dialog') ?? document.body)
        : null;
    const displayText = formatDisplay(currentValue);

    return (
      <div
        ref={wrapRef}
        className={`evia-input evia-datetime ${className}`}
        data-open={open || undefined}
        data-disabled={disabled || undefined}
        data-readonly={readOnly || undefined}
        data-invalid={props['aria-invalid'] ? 'true' : undefined}
      >
        <span
          className={`evia-datetime-value${displayText ? '' : ' evia-datetime-placeholder'}`}
          aria-hidden="true"
        >
          {displayText || '选择日期与时间'}
        </span>
        <span className="evia-datetime-icon" aria-hidden="true">
          <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
            <rect
              x="2"
              y="3"
              width="12"
              height="11"
              rx="2.2"
              stroke="currentColor"
              strokeWidth="1.4"
            />
            <path
              d="M5 1.8v2.4M11 1.8v2.4M2 6.5h12"
              stroke="currentColor"
              strokeWidth="1.4"
              strokeLinecap="round"
            />
          </svg>
        </span>
        <input
          {...props}
          ref={inputRef}
          type="datetime-local"
          className="evia-datetime-native"
          value={value}
          defaultValue={defaultValue}
          disabled={disabled}
          readOnly={readOnly}
          required={required}
          min={min}
          max={max}
          aria-expanded={open}
          aria-controls={open ? popoverID : undefined}
          onMouseDown={handleMouseDown}
          onKeyDown={handleKeyDown}
          onChange={(event) => {
            if (value === undefined) setUncontrolledValue(event.target.value);
            onChange?.(event);
          }}
          onBlur={(event) => {
            if (
              popoverRef.current &&
              event.relatedTarget instanceof Node &&
              popoverRef.current.contains(event.relatedTarget)
            )
              return;
            setOpen(false);
            onBlur?.(event);
          }}
        />
        {open &&
          portalTarget &&
          createPortal(
            <div
              ref={popoverRef}
              id={popoverID}
              role="dialog"
              aria-label="选择日期与时间"
              className="evia-datetime-popover"
              style={popoverStyle}
              onMouseDown={(event) => event.preventDefault()}
            >
              <div className="evia-datetime-body">
                <div className="evia-datetime-calendar">
                  <div className="evia-datetime-cal-head">
                    <button
                      type="button"
                      className="evia-datetime-nav"
                      aria-label="上个月"
                      tabIndex={-1}
                      onClick={() => stepMonth(-1)}
                    >
                      <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                        <path
                          d="M10 3.5 5.5 8 10 12.5"
                          stroke="currentColor"
                          strokeWidth="1.6"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                        />
                      </svg>
                    </button>
                    <span className="evia-datetime-month">
                      {viewYear} 年 {viewMonth} 月
                    </span>
                    <button
                      type="button"
                      className="evia-datetime-nav"
                      aria-label="下个月"
                      tabIndex={-1}
                      onClick={() => stepMonth(1)}
                    >
                      <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                        <path
                          d="M6 3.5 10.5 8 6 12.5"
                          stroke="currentColor"
                          strokeWidth="1.6"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                        />
                      </svg>
                    </button>
                  </div>

                  <div className="evia-datetime-weekdays" aria-hidden="true">
                    {WEEKDAYS.map((day) => (
                      <span key={day}>{day}</span>
                    ))}
                  </div>

                  <div className="evia-datetime-days">
                    {cells.map((cell) => {
                      const key = dateKey(cell.year, cell.month, cell.day);
                      const isSelected =
                        parsed?.year === cell.year &&
                        parsed?.month === cell.month &&
                        parsed?.day === cell.day;
                      const isToday =
                        today.year === cell.year &&
                        today.month === cell.month &&
                        today.day === cell.day;
                      const outOfBounds =
                        (minDateKey !== '' && key < minDateKey) ||
                        (maxDateKey !== '' && key > maxDateKey);
                      return (
                        <button
                          key={key}
                          type="button"
                          tabIndex={-1}
                          disabled={outOfBounds}
                          className="evia-datetime-day"
                          data-outside={cell.outside || undefined}
                          data-today={isToday || undefined}
                          aria-selected={isSelected}
                          onClick={() => pickDay(cell.year, cell.month, cell.day)}
                        >
                          {cell.day}
                        </button>
                      );
                    })}
                  </div>
                </div>

                <div className="evia-datetime-time">
                  <div className="evia-datetime-time-col">
                    <span className="evia-datetime-time-label">时</span>
                    <div ref={hourListRef} className="evia-datetime-time-list">
                      {HOURS.map((hour) => (
                        <button
                          key={hour}
                          type="button"
                          tabIndex={-1}
                          className="evia-datetime-time-item"
                          aria-selected={parsed?.hour === hour}
                          onClick={() => pickHour(hour)}
                        >
                          {pad2(hour)}
                        </button>
                      ))}
                    </div>
                  </div>
                  <div className="evia-datetime-time-col">
                    <span className="evia-datetime-time-label">分</span>
                    <div ref={minuteListRef} className="evia-datetime-time-list">
                      {MINUTES.map((minute) => (
                        <button
                          key={minute}
                          type="button"
                          tabIndex={-1}
                          className="evia-datetime-time-item"
                          aria-selected={parsed?.minute === minute}
                          onClick={() => pickMinute(minute)}
                        >
                          {pad2(minute)}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>
              </div>

              <div className="evia-datetime-footer">
                <div className="evia-datetime-footer-left">
                  <button
                    type="button"
                    tabIndex={-1}
                    className="evia-datetime-link"
                    onClick={pickNow}
                  >
                    此刻
                  </button>
                  {!required && currentValue && (
                    <button
                      type="button"
                      tabIndex={-1}
                      className="evia-datetime-link"
                      onClick={() => commitValue('')}
                    >
                      清空
                    </button>
                  )}
                </div>
                <button
                  type="button"
                  tabIndex={-1}
                  className="evia-datetime-done"
                  onClick={() => setOpen(false)}
                >
                  确定
                </button>
              </div>
            </div>,
            portalTarget,
          )}
      </div>
    );
  },
);
