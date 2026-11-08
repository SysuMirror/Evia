import { useEffect, useRef, type MouseEvent } from 'react';
import type { SavedAnalysisDefinition } from '@evia/api-client';

/** Keep focus useful after row removal while yielding to newer pointer/keyboard ownership. */
export function useSectionFocus(candidate: SavedAnalysisDefinition) {
  const keepButton = useRef<HTMLButtonElement>(null);
  const jsonRef = useRef<HTMLTextAreaElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const removedFocus = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    const source = removedFocus.current;
    removedFocus.current = null;
    if (!source) return;
    let interrupted = false;
    const interrupt = () => {
      interrupted = true;
    };
    document.addEventListener('pointerdown', interrupt, true);
    document.addEventListener('keydown', interrupt, true);
    const frame = requestAnimationFrame(() => {
      if (
        !interrupted &&
        (!source.isConnected || source.disabled) &&
        (document.activeElement === document.body || document.activeElement === source) &&
        document.hasFocus()
      ) {
        const target = [
          ...(formRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []),
        ].find((button) => button.textContent?.trim().startsWith('添加'));
        target?.focus();
      }
      document.removeEventListener('pointerdown', interrupt, true);
      document.removeEventListener('keydown', interrupt, true);
    });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener('pointerdown', interrupt, true);
      document.removeEventListener('keydown', interrupt, true);
    };
  }, [candidate]);
  function captureRemoval(event: MouseEvent<HTMLFormElement>) {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>('button');
    if (button?.textContent?.trim().startsWith('移除')) removedFocus.current = button;
  }
  function focusError() {
    requestAnimationFrame(() =>
      formRef.current
        ?.querySelector<HTMLElement>('[aria-invalid="true"], textarea, input, select')
        ?.focus(),
    );
  }
  return { keepButton, jsonRef, formRef, captureRemoval, focusError };
}
