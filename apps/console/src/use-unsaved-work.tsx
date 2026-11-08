import { useCallback, useEffect, useRef, useState } from 'react';
import { useBlocker } from 'react-router-dom';
import { Button, Modal } from '@evia/design-system';
import { useDraftNavigation } from './draft-navigation';

/** Mount once per page and render confirmation alongside that page's dialogs. */
export function useUnsavedWork({
  dirty,
  busy = false,
  onDiscard,
}: {
  dirty: boolean;
  busy?: boolean;
  onDiscard?: () => void;
}) {
  const { register } = useDraftNavigation();
  const [confirming, setConfirming] = useState(false);
  const continueButton = useRef<HTMLButtonElement>(null);
  const pendingAction = useRef<(() => void) | null>(null);
  const afterClose = useRef<(() => void) | null>(null);
  const approved = useRef(false);
  const dismissedBlocker = useRef(false);
  const mounted = useRef(false);
  const state = useRef({ dirty, busy, onDiscard, confirming });
  state.current = { dirty, busy, onDiscard, confirming };
  const blocker = useBlocker(
    () => !approved.current && (state.current.dirty || state.current.busy),
  );

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      pendingAction.current = null;
      afterClose.current = null;
    };
  }, []);

  const requestLeave = useCallback((action: () => void) => {
    if (state.current.busy || state.current.confirming || afterClose.current) return;
    if (state.current.dirty) {
      pendingAction.current = action;
      state.current.confirming = true;
      setConfirming(true);
    } else action();
  }, []);

  useEffect(() => register(requestLeave), [register, requestLeave]);

  useEffect(() => {
    if (!dirty && !busy) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty, busy]);

  useEffect(() => {
    if (blocker.state !== 'blocked') {
      dismissedBlocker.current = false;
      return;
    }
    if (dismissedBlocker.current || afterClose.current) return;
    if (busy) {
      dismissedBlocker.current = true;
      blocker.reset();
    } else if (!dirty && !confirming) {
      blocker.proceed();
    } else {
      setConfirming(true);
    }
  }, [blocker, busy, dirty, confirming]);

  const cancel = () => {
    if (state.current.busy) return;
    pendingAction.current = null;
    afterClose.current = null;
    if (blocker.state === 'blocked') {
      dismissedBlocker.current = true;
      blocker.reset();
    }
    setConfirming(false);
  };

  const discard = () => {
    if (state.current.busy) return;
    const proceed = blocker.state === 'blocked' ? blocker.proceed : pendingAction.current;
    pendingAction.current = null;
    afterClose.current = proceed;
    state.current.onDiscard?.();
    setConfirming(false);
  };

  const confirmation = (
    <Modal
      open={confirming}
      title="放弃未保存的修改？"
      onRequestClose={cancel}
      busy={busy}
      initialFocusRef={continueButton}
      afterClose={() => {
        if (!mounted.current) return;
        const action = afterClose.current;
        afterClose.current = null;
        if (!action) return;
        approved.current = true;
        try {
          action();
        } finally {
          approved.current = false;
        }
      }}
      footer={
        <>
          <Button ref={continueButton} disabled={busy} onClick={cancel}>
            继续编辑
          </Button>
          <Button variant="danger" disabled={busy} onClick={discard}>
            放弃修改
          </Button>
        </>
      }
    >
      <p>离开后，未保存的内容将丢失。</p>
    </Modal>
  );

  return { requestLeave, confirmation };
}
