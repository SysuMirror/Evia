import { useEffect, useRef, type RefObject } from 'react';

/** A disappearing recovery control may return focus only while it still owns the interaction. */
export function useCompletionFocus() {
  const cleanups = useRef(new Set<() => void>());
  useEffect(
    () => () => {
      for (const cleanup of cleanups.current) cleanup();
    },
    [],
  );
  return (source: HTMLElement) => {
    let owned = document.activeElement === source;
    let frame: number | undefined;
    const relinquish = () => {
      owned = false;
    };
    const move = (event: FocusEvent) => {
      if (event.target !== source && event.target !== document.body) relinquish();
    };
    const cleanup = () => {
      if (frame !== undefined) cancelAnimationFrame(frame);
      document.removeEventListener('pointerdown', relinquish, true);
      document.removeEventListener('keydown', relinquish, true);
      document.removeEventListener('focusin', move);
      window.removeEventListener('blur', relinquish);
      cleanups.current.delete(cleanup);
    };
    document.addEventListener('pointerdown', relinquish, true);
    document.addEventListener('keydown', relinquish, true);
    document.addEventListener('focusin', move);
    window.addEventListener('blur', relinquish);
    cleanups.current.add(cleanup);
    return {
      cancel: cleanup,
      restore: (target: RefObject<HTMLElement | null>) => {
        frame = requestAnimationFrame(() => {
          if (
            owned &&
            document.hasFocus() &&
            (!source.isConnected || source.matches(':disabled')) &&
            (document.activeElement === document.body || document.activeElement === source) &&
            target.current?.isConnected &&
            !target.current.matches(':disabled')
          )
            target.current.focus({ preventScroll: true });
          cleanup();
        });
      },
    };
  };
}
