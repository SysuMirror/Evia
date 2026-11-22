import { useId, useLayoutEffect, useRef, type ReactNode, type RefObject } from 'react';
import { Button } from './controls';

const dialogs: HTMLDialogElement[] = [];
interface ClosingDialog {
  dialog: HTMLDialogElement;
  target: HTMLElement | null;
  fallback: HTMLElement | null;
  afterClose?: () => void;
}
const closing = new Map<HTMLDialogElement, ClosingDialog>();
let restorationFrame: number | undefined;

function restoreAfterClose(entry: ClosingDialog) {
  closing.set(entry.dialog, entry);
  if (restorationFrame !== undefined) return;
  // Closing is asynchronous in React. A new pointer/key action or window blur owns
  // focus from this point onward, even when it leaves document.body active.
  let interrupted = false;
  const relinquish = () => {
    interrupted = true;
  };
  document.addEventListener('pointerdown', relinquish, true);
  document.addEventListener('keydown', relinquish, true);
  window.addEventListener('blur', relinquish);
  restorationFrame = requestAnimationFrame(() => {
    restorationFrame = undefined;
    document.removeEventListener('pointerdown', relinquish, true);
    document.removeEventListener('keydown', relinquish, true);
    window.removeEventListener('blur', relinquish);
    const entries = [...closing.values()];
    closing.clear();
    const top = dialogs.at(-1);
    const active = document.activeElement;
    const hasNewTarget =
      active instanceof HTMLElement &&
      active !== document.body &&
      active !== document.documentElement &&
      active.isConnected &&
      !active.closest('dialog:not([open]), [inert]') &&
      !entries.some((item) => item.dialog.contains(active));
    const target = entries
      .flatMap((item) => [item.target, item.fallback])
      .find((candidate) => {
        if (!candidate?.isConnected || candidate.closest('dialog:not([open])')) return false;
        if (top && !top.contains(candidate)) return false;
        return candidate.getClientRects().length > 0 && !candidate.matches(':disabled');
      });
    if (!interrupted && !hasNewTarget && document.hasFocus())
      target?.focus({ preventScroll: true });
    entries.forEach((item) => item.afterClose?.());
  });
}

export interface ModalProps {
  open: boolean;
  title: ReactNode;
  onRequestClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  initialFocusRef?: RefObject<HTMLElement | null>;
  busy?: boolean;
  size?: 'default' | 'wide';
  afterClose?: () => void;
}

export function Modal({
  open,
  title,
  onRequestClose,
  children,
  footer,
  initialFocusRef,
  busy = false,
  size = 'default',
  afterClose,
}: ModalProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const triggerRef = useRef<HTMLElement | null>(null);
  const fallbackRef = useRef<HTMLElement | null>(null);
  const backdropPointer = useRef<number | null>(null);
  const callbacks = useRef({ onRequestClose, afterClose, busy, initialFocusRef });
  callbacks.current = { onRequestClose, afterClose, busy, initialFocusRef };
  const titleID = useId();

  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog || !open) return;
    // StrictMode may immediately reopen this same dialog. Keep its original trigger.
    const interrupted = closing.get(dialog);
    closing.delete(dialog);
    triggerRef.current =
      interrupted?.target ??
      (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    fallbackRef.current =
      interrupted?.fallback ?? triggerRef.current?.closest<HTMLElement>('dialog, main') ?? null;
    dialog.showModal();
    dialogs.push(dialog);
    const initial = callbacks.current.initialFocusRef?.current;
    const firstControl = dialog.querySelector<HTMLElement>(
      'input:not([disabled]), textarea:not([disabled]), select:not([disabled]), button:not([disabled]):not([data-modal-close]), [tabindex="0"]',
    );
    (initial ?? firstControl ?? dialog).focus({ preventScroll: true });
    return () => {
      const index = dialogs.indexOf(dialog);
      if (index !== -1) dialogs.splice(index, 1);
      dialog.close();
      restoreAfterClose({
        dialog,
        target: triggerRef.current,
        fallback: fallbackRef.current,
        afterClose: callbacks.current.afterClose,
      });
    };
  }, [open]);

  return (
    <dialog
      ref={dialogRef}
      className={`evia-modal evia-modal-${size}`}
      aria-labelledby={titleID}
      aria-busy={busy || undefined}
      tabIndex={-1}
      onPointerDown={(event) => {
        const bounds = event.currentTarget.getBoundingClientRect();
        backdropPointer.current =
          event.target === event.currentTarget &&
          (event.clientX < bounds.left ||
            event.clientX > bounds.right ||
            event.clientY < bounds.top ||
            event.clientY > bounds.bottom)
            ? event.pointerId
            : null;
      }}
      onPointerCancel={() => {
        backdropPointer.current = null;
      }}
      onPointerUp={(event) => {
        const beganOnBackdrop = backdropPointer.current === event.pointerId;
        backdropPointer.current = null;
        const bounds = event.currentTarget.getBoundingClientRect();
        if (
          beganOnBackdrop &&
          event.target === event.currentTarget &&
          (event.clientX < bounds.left ||
            event.clientX > bounds.right ||
            event.clientY < bounds.top ||
            event.clientY > bounds.bottom) &&
          !callbacks.current.busy &&
          dialogs.at(-1) === event.currentTarget
        )
          callbacks.current.onRequestClose();
      }}
      onCancel={(event) => {
        event.preventDefault();
        if (!callbacks.current.busy && dialogs.at(-1) === dialogRef.current)
          callbacks.current.onRequestClose();
      }}
    >
      <header className="evia-modal-header">
        <h2 id={titleID}>{title}</h2>
        <Button
          variant="ghost"
          size="small"
          iconOnly
          startIcon={
            <svg width="18" height="18" viewBox="0 0 18 18" fill="none" aria-hidden="true">
              <path
                d="m4.5 4.5 9 9m0-9-9 9"
                stroke="currentColor"
                strokeWidth="1.6"
                strokeLinecap="round"
              />
            </svg>
          }
          className="evia-modal-close"
          data-modal-close
          aria-label="关闭"
          disabled={busy}
          onClick={onRequestClose}
        />
      </header>
      <div className="evia-modal-body">{children}</div>
      {footer && <footer className="evia-modal-footer">{footer}</footer>}
    </dialog>
  );
}
