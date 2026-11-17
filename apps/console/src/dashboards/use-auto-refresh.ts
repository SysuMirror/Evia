import { useEffect, useRef, useState } from 'react';

export const AUTO_REFRESH_MIN_SECONDS = 60;
export const AUTO_REFRESH_OPTIONS = [
  { value: 0, label: '关闭自动刷新' },
  { value: 60, label: '每 60 秒' },
  { value: 300, label: '每 5 分钟' },
] as const;

/**
 * Bounded auto-refresh for the dashboard run view.
 *
 * Invariants from the dashboard plan:
 * - the shortest interval is 60 seconds, with ±10% jitter so many tabs do not
 *   synchronize into a request burst;
 * - a refresh never overlaps an in-flight run or a frozen control state — the
 *   tick re-checks shortly instead of enqueuing a second run;
 * - hidden tabs stop scheduling and re-arm only after becoming visible again;
 * - switching the interval off or unmounting clears every timer.
 */
export function useDashboardAutoRefresh({
  intervalSeconds,
  blocked,
  onRefresh,
}: {
  intervalSeconds: number;
  blocked: boolean;
  onRefresh: () => void;
}) {
  const [nextAt, setNextAt] = useState<number | null>(null);
  const blockedRef = useRef(blocked);
  blockedRef.current = blocked;
  const refreshRef = useRef(onRefresh);
  refreshRef.current = onRefresh;

  useEffect(() => {
    if (intervalSeconds < AUTO_REFRESH_MIN_SECONDS) {
      setNextAt(null);
      return;
    }
    let timer: number | undefined;
    let cancelled = false;

    const jitteredDelay = () => {
      const base = intervalSeconds * 1000;
      const jitter = base * 0.1 * (Math.random() * 2 - 1);
      return Math.max(AUTO_REFRESH_MIN_SECONDS * 1000, Math.round(base + jitter));
    };
    const arm = (delayMs: number) => {
      if (cancelled) return;
      setNextAt(Date.now() + delayMs);
      timer = window.setTimeout(tick, delayMs);
    };
    const tick = () => {
      if (cancelled || document.hidden) return;
      if (blockedRef.current) {
        // A run is queued/running or controls are frozen; retry shortly so
        // refreshes queue behind the active run instead of overlapping it.
        arm(5000);
        return;
      }
      refreshRef.current();
      arm(jitteredDelay());
    };
    const onVisibility = () => {
      if (cancelled || document.hidden) return;
      window.clearTimeout(timer);
      arm(jitteredDelay());
    };

    document.addEventListener('visibilitychange', onVisibility);
    arm(jitteredDelay());
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisibility);
      setNextAt(null);
    };
  }, [intervalSeconds]);

  return { nextAt };
}
