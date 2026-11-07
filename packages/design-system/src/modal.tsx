import { useId, useLayoutEffect, useRef, type ReactNode, type RefObject } from 'react';
import { Button } from './controls';

const dialogs: HTMLDialogElement[] = [];
interface ClosingDialog {
  dialog: HTMLDialogElement;
  target: HTMLElement | null;
  afterClose?: () => void;
}
const closing = new Map<HTMLDialogElement, ClosingDialog>();
let restorationFrame: number | undefined;

function restoreAfterClose(entry: ClosingDialog) {
  closing.set(entry.dialog, entry);
  if (restorationFrame !== undefined) return;
  restorationFrame = requestAnimationFrame(() => {
    restorationFrame = undefined;
    const entries = [...closing.values()];
    closing.clear();
    const top = dialogs.at(-1);
    const target = entries
      .map((item) => item.target)
      .find((candidate) => {
        if (!candidate?.isConnected || candidate.closest('dialog:not([open])')) return false;
        if (top && !top.contains(candidate)) return false;
        return candidate.getClientRects().length > 0 && !candidate.matches(':disabled');
      });
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
  afterClose,
}: ModalProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const triggerRef = useRef<HTMLElement | null>(null);
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
        afterClose: callbacks.current.afterClose,
      });
    };
  }, [open]);

  return (
    <dialog
      ref={dialogRef}
      className="evia-modal"
      aria-labelledby={titleID}
      aria-busy={busy || undefined}
      tabIndex={-1}
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
          className="evia-modal-close"
          data-modal-close
          aria-label="关闭"
          disabled={busy}
          onClick={onRequestClose}
        >
          ×
        </Button>
      </header>
      <div className="evia-modal-body">{children}</div>
      {footer && <footer className="evia-modal-footer">{footer}</footer>}
    </dialog>
  );
}
