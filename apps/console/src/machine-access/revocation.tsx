import { useRef, useState } from 'react';
import {
  api,
  ApiError,
  type MachineCredential,
  type MachinePrincipal,
  type Session,
} from '@evia/api-client';
import { Alert, Button, Field, Input, Modal } from '@evia/design-system';
import { MachineFailure } from './failure';
import { environmentLabels, uncertain } from './model';
import type { PerformMachineRequest } from './use-request';
import { useCompletionFocus } from './use-completion-focus';

export type RevocationTarget = { principal: MachinePrincipal; credential?: MachineCredential };
export function MachineRevocation({
  target,
  projectID,
  session,
  busy,
  perform,
  onClose,
  onComplete,
}: {
  target: RevocationTarget;
  projectID: string;
  session: Session;
  busy: string;
  perform: PerformMachineRequest;
  onClose: () => void;
  onComplete: () => void;
}) {
  const [principal, setPrincipal] = useState(target.principal);
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState<Error>();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const confirmationRef = useRef<HTMLInputElement>(null);
  const captureFocus = useCompletionFocus();
  const credential = target.credential;
  const conflict = error instanceof ApiError && error.code === 'MACHINE_REVISION_CONFLICT';
  return (
    <Modal
      open
      title={credential ? '撤销机器凭据' : '撤销机器'}
      onRequestClose={onClose}
      busy={!!busy}
      initialFocusRef={cancelRef}
      footer={
        <>
          <Button ref={cancelRef} disabled={!!busy} onClick={onClose}>
            取消
          </Button>
          <Button
            variant="danger"
            loading={busy === 'revoke'}
            disabled={!!busy || confirmation !== principal.name || conflict}
            onClick={() => {
              setError(undefined);
              void perform(
                'revoke',
                (signal) =>
                  credential
                    ? api.revokeMachineCredential(
                        projectID,
                        principal.id,
                        credential.id,
                        principal.revision,
                        session.csrf_token,
                        signal,
                      )
                    : api.revokeMachinePrincipal(
                        projectID,
                        principal.id,
                        principal.revision,
                        session.csrf_token,
                        signal,
                      ),
                onComplete,
                setError,
              );
            }}
          >
            确认撤销
          </Button>
        </>
      }
    >
      {error && <MachineFailure error={error} />}
      {uncertain(error) && (
        <Alert tone="warning" title="撤销结果尚未确认">
          请重新读取详情确认当前状态。若仍有效，可以再次提交撤销。
        </Alert>
      )}
      {(conflict || uncertain(error)) && (
        <Button
          disabled={!!busy}
          onClick={(event) => {
            const focus = captureFocus(event.currentTarget);
            void perform(
              'refresh-revocation',
              (signal) => api.getMachinePrincipal(projectID, principal.id, signal),
              (detail) => {
                const found =
                  credential && detail.credentials.find((item) => item.id === credential.id);
                if (detail.principal.revoked_at || (credential && (!found || found.revoked_at))) {
                  focus.cancel();
                  onComplete();
                } else {
                  setPrincipal(detail.principal);
                  setError(undefined);
                  setConfirmation('');
                  focus.restore(confirmationRef);
                }
              },
              (failure) => {
                focus.cancel();
                setError(failure);
              },
            );
          }}
        >
          重新读取详情
        </Button>
      )}
      <p className="machine-scope">
        <strong>{principal.name}</strong>
        <span>{environmentLabels[principal.environment]}</span>
      </p>
      <Alert tone="warning" title="撤销后无法恢复">
        {credential
          ? '此凭据将立即失效，此机器的其他有效凭据仍可使用。'
          : '此机器的所有凭据将立即失效，无法重新启用。已有事件、发布、分析和看板记录会保留。'}
      </Alert>
      {credential && (
        <p>
          凭据标识：<code className="machine-wrap">{credential.id}</code>
        </p>
      )}
      <Field label="输入机器名称以确认" htmlFor="machine-revoke-name">
        <Input
          ref={confirmationRef}
          id="machine-revoke-name"
          autoComplete="off"
          value={confirmation}
          disabled={!!busy}
          onChange={(event) => setConfirmation(event.target.value)}
        />
      </Field>
    </Modal>
  );
}
