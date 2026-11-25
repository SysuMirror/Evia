import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { Button } from './controls';

export type Tone = 'info' | 'success' | 'warning' | 'error';

export function Alert({
  tone = 'info',
  title,
  children,
  action,
}: {
  tone?: Tone;
  title: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className={`evia-alert evia-alert-${tone}`} role={tone === 'error' ? 'alert' : 'status'}>
      <div className="evia-alert-content">
        <strong>{title}</strong>
        {children && <div className="evia-alert-description">{children}</div>}
      </div>
      {action && <div className="evia-alert-action">{action}</div>}
    </div>
  );
}

export function Badge({
  tone = 'info',
  children,
}: {
  tone?: Tone | 'neutral';
  children: ReactNode;
}) {
  return <span className={`evia-badge evia-badge-${tone}`}>{children}</span>;
}

interface ToastAPI {
  success: (message: string) => void;
  error: (message: string) => void;
}
interface Toast {
  id: number;
  tone: 'success' | 'error';
  message: string;
}
const ToastContext = createContext<ToastAPI | null>(null);

function canRestoreFocus(element: HTMLElement | null | undefined): element is HTMLElement {
  return Boolean(
    element &&
    element !== document.body &&
    element !== document.documentElement &&
    element.isConnected &&
    element.getClientRects().length > 0 &&
    !element.matches(':disabled') &&
    !element.closest('dialog:not([open]), [inert]'),
  );
}

export function DesignProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextID = useRef(0);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());
  const elements = useRef(new Map<number, HTMLDivElement>());
  const origins = useRef(new Map<number, HTMLElement | null>());
  const pendingOrigins = useRef(new Set<number>());
  const focusedToast = useRef<number | null>(null);
  const hoveredToast = useRef<number | null>(null);
  const dismiss = useCallback((id: number) => {
    const restore = elements.current.get(id)?.contains(document.activeElement);
    const origin = origins.current.get(id);
    setToasts((current) => current.filter((toast) => toast.id !== id));
    if (restore)
      requestAnimationFrame(() => {
        if (
          document.activeElement === document.body &&
          canRestoreFocus(origin) &&
          !document.querySelector('dialog:modal')
        )
          origin.focus({ preventScroll: true });
      });
  }, []);
  const add = useCallback((tone: Toast['tone'], message: string) => {
    const id = ++nextID.current;
    const protectedIDs = new Set([focusedToast.current, hoveredToast.current]);
    origins.current.set(
      id,
      document.activeElement instanceof HTMLElement ? document.activeElement : null,
    );
    pendingOrigins.current.add(id);
    setToasts((current) => {
      const next = [...current, { id, tone, message }];
      while (next.length > 5) {
        const index = next.findIndex((toast) => !protectedIDs.has(toast.id));
        next.splice(index, 1);
      }
      return next;
    });
  }, []);
  useEffect(() => {
    const visible = new Set(toasts.map((toast) => toast.id));
    for (const id of origins.current.keys()) {
      if (!visible.has(id)) {
        origins.current.delete(id);
        pendingOrigins.current.delete(id);
      }
    }
    for (const [id, timer] of timers.current) {
      if (!visible.has(id)) {
        clearTimeout(timer);
        timers.current.delete(id);
      }
    }
    function schedule(id: number) {
      timers.current.set(
        id,
        setTimeout(() => {
          const element = elements.current.get(id);
          if (element && (element.contains(document.activeElement) || element.matches(':hover'))) {
            schedule(id);
          } else {
            timers.current.delete(id);
            dismiss(id);
          }
        }, 4500),
      );
    }
    for (const toast of toasts) {
      if (toast.tone === 'success' && !timers.current.has(toast.id)) schedule(toast.id);
    }
    const frame = requestAnimationFrame(() => {
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      for (const id of pendingOrigins.current) {
        // A success may close a dialog. Observe its restored focus before recording a return target.
        if (
          !canRestoreFocus(origins.current.get(id)) &&
          canRestoreFocus(active) &&
          !active.closest('.evia-toast-region')
        )
          origins.current.set(id, active);
      }
      pendingOrigins.current.clear();
    });
    return () => cancelAnimationFrame(frame);
  }, [toasts, dismiss]);
  const api = useMemo(
    () => ({
      success: (message: string) => add('success', message),
      error: (message: string) => add('error', message),
    }),
    [add],
  );
  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach(clearTimeout);
      pending.clear();
    };
  }, []);
  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="evia-toast-region" aria-label="操作通知">
        {toasts.map((toast) => (
          <div
            key={toast.id}
            className={`evia-toast evia-toast-${toast.tone}`}
            role={toast.tone === 'error' ? 'alert' : 'status'}
            ref={(element) => {
              if (element) elements.current.set(toast.id, element);
              else elements.current.delete(toast.id);
            }}
            onFocusCapture={() => {
              focusedToast.current = toast.id;
            }}
            onPointerEnter={() => {
              hoveredToast.current = toast.id;
            }}
            onPointerLeave={() => {
              if (hoveredToast.current === toast.id) hoveredToast.current = null;
            }}
            onBlurCapture={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget)) focusedToast.current = null;
            }}
          >
            <span>{toast.message}</span>
            <Button
              variant="ghost"
              size="small"
              iconOnly
              startIcon={
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                  <path
                    d="m4 4 8 8m0-8-8 8"
                    stroke="currentColor"
                    strokeWidth="1.65"
                    strokeLinecap="round"
                  />
                </svg>
              }
              onClick={() => dismiss(toast.id)}
              aria-label="关闭通知"
            />
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastAPI {
  const value = useContext(ToastContext);
  if (!value) throw new Error('useToast requires DesignProvider');
  return value;
}

export function CopyButton({ value, label = '复制' }: { value: string; label?: string }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState('');
  const pending = useRef(false);
  const operation = useRef(0);
  useLayoutEffect(() => {
    pending.current = false;
    setBusy(false);
    setFeedback('');
    return () => {
      ++operation.current;
      pending.current = false;
    };
  }, [value]);
  async function copy() {
    if (pending.current) return;
    pending.current = true;
    const attempt = ++operation.current;
    setBusy(true);
    setFeedback('');
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(value);
      if (attempt !== operation.current) return;
      toast.success('已复制');
      setFeedback('已复制');
    } catch {
      if (attempt !== operation.current) return;
      toast.error('复制失败，请选择内容后手动复制');
      setFeedback('复制失败，请选择内容后手动复制');
    } finally {
      if (attempt === operation.current) {
        pending.current = false;
        setBusy(false);
      }
    }
  }
  return (
    <span className="evia-copy">
      <Button
        variant="ghost"
        size="small"
        onClick={() => {
          void copy();
        }}
        loading={busy}
      >
        {label}
      </Button>
      <span className="evia-copy-feedback" role="status">
        {feedback}
      </span>
    </span>
  );
}
