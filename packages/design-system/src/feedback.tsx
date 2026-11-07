import {
  createContext,
  useCallback,
  useContext,
  useEffect,
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

export function DesignProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextID = useRef(0);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());
  const dismiss = useCallback((id: number) => {
    clearTimeout(timers.current.get(id));
    timers.current.delete(id);
    setToasts((current) => current.filter((toast) => toast.id !== id));
  }, []);
  const add = useCallback(
    (tone: Toast['tone'], message: string) => {
      const id = ++nextID.current;
      setToasts((current) => {
        const next = [...current, { id, tone, message }];
        for (const evicted of next.slice(0, -5)) {
          clearTimeout(timers.current.get(evicted.id));
          timers.current.delete(evicted.id);
        }
        return next.slice(-5);
      });
      if (tone === 'success')
        timers.current.set(
          id,
          setTimeout(() => dismiss(id), 4500),
        );
    },
    [dismiss],
  );
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
          >
            <span>{toast.message}</span>
            <Button
              variant="ghost"
              size="small"
              onClick={() => dismiss(toast.id)}
              aria-label="关闭通知"
            >
              ×
            </Button>
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
  async function copy() {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setFeedback('');
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(value);
      toast.success('已复制');
      setFeedback('已复制');
    } catch {
      toast.error('复制失败，请选择内容后手动复制');
      setFeedback('复制失败，请选择内容后手动复制');
    } finally {
      pending.current = false;
      setBusy(false);
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
