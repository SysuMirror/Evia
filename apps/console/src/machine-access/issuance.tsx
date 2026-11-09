import { useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  type Environment,
  type MachineIssued,
  type MachinePrincipal,
  type MachinePrincipalInput,
  type MachineCredentialRotation,
  type Session,
} from '@evia/api-client';
import { Alert, Button, Field, Input, Modal, Select } from '@evia/design-system';
import { MachineFailure } from './failure';
import { MachinePermissions } from './permissions';
import { environmentLabels, localExpiry, uncertain, validateExpiry } from './model';
import type { PerformMachineRequest } from './use-request';

export type IssuanceIntent = { kind: 'create' } | { kind: 'rotate'; principal: MachinePrincipal };
type Attempt =
  | { kind: 'create'; key: string; body: MachinePrincipalInput }
  | {
      kind: 'rotate';
      key: string;
      principalID: string;
      revision: number;
      body: MachineCredentialRotation;
    };

export function MachineIssuance({
  open,
  intent,
  projectID,
  environment,
  session,
  busy,
  perform,
  onClose,
  onDiscard,
  onDirtyChange,
  onIssued,
  onConflict,
}: {
  open: boolean;
  intent: IssuanceIntent;
  projectID: string;
  environment: Environment;
  session: Session;
  busy: string;
  perform: PerformMachineRequest;
  onClose: () => void;
  onDiscard: () => void;
  onDirtyChange: (dirty: boolean) => void;
  onIssued: (result: MachineIssued) => void;
  onConflict: () => void;
}) {
  const [name, setName] = useState('');
  const [selectedEnvironment, setSelectedEnvironment] = useState(environment);
  const [permissions, setPermissions] = useState<MachinePrincipalInput['permissions']>([]);
  const [expiry, setExpiry] = useState(() => localExpiry());
  const [overlap, setOverlap] = useState('0');
  const [attempt, setAttempt] = useState<Attempt>();
  const [error, setError] = useState<Error>();
  const [fields, setFields] = useState<{
    name?: string;
    permissions?: string;
    expiry?: string;
    overlap?: string;
  }>({});
  const nameRef = useRef<HTMLInputElement>(null);
  const expiryRef = useRef<HTMLInputElement>(null);
  const overlapRef = useRef<HTMLInputElement>(null);
  const permissionsRef = useRef<HTMLDivElement>(null);
  const pristineExpiry = useRef(expiry);
  const submitting = useRef(false);
  const isCreate = intent.kind === 'create';
  const pending = !!attempt && uncertain(error);
  const dirty =
    !!attempt ||
    name !== '' ||
    selectedEnvironment !== environment ||
    permissions.length > 0 ||
    expiry !== pristineExpiry.current ||
    overlap !== '0';
  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);
  const disabled = !!busy || pending;
  const submit = () => {
    if (busy || submitting.current) return;
    let next = attempt;
    if (!next) {
      const errors = {
        name:
          isCreate && !name.trim()
            ? '请输入机器名称'
            : isCreate && [...name.trim()].length > 128
              ? '机器名称最多为 128 个字符'
              : undefined,
        permissions: isCreate && permissions.length === 0 ? '请至少选择一项权限' : undefined,
        expiry: validateExpiry(expiry),
        overlap:
          !isCreate && (!/^\d+$/.test(overlap) || Number(overlap) > 300)
            ? '重叠时间为 0–300 秒的整数'
            : undefined,
      };
      setFields(errors);
      if (Object.values(errors).some(Boolean)) {
        if (errors.name) nameRef.current?.focus();
        else if (errors.permissions) permissionsRef.current?.focus();
        else if (errors.expiry) expiryRef.current?.focus();
        else overlapRef.current?.focus();
        return;
      }
      next = isCreate
        ? {
            kind: 'create',
            key: crypto.randomUUID(),
            body: {
              name: name.trim(),
              environment: selectedEnvironment,
              permissions: [...permissions],
              expires_at: new Date(expiry).toISOString(),
            },
          }
        : {
            kind: 'rotate',
            key: crypto.randomUUID(),
            principalID: intent.principal.id,
            revision: intent.principal.revision,
            body: { expires_at: new Date(expiry).toISOString(), overlap_seconds: Number(overlap) },
          };
      setAttempt(next);
    }
    const request = next;
    submitting.current = true;
    if (!pending) setError(undefined);
    void perform(
      'issuance',
      (signal) =>
        request.kind === 'create'
          ? api.createMachinePrincipal(
              projectID,
              request.body,
              request.key,
              session.csrf_token,
              signal,
            )
          : api.rotateMachineCredential(
              projectID,
              request.principalID,
              request.revision,
              request.body,
              request.key,
              session.csrf_token,
              signal,
            ),
      onIssued,
      (failure) => {
        setError(failure);
        if (!uncertain(failure)) setAttempt(undefined);
        if (failure instanceof ApiError && failure.code === 'MACHINE_REVISION_CONFLICT')
          onConflict();
      },
    ).finally(() => {
      submitting.current = false;
    });
  };
  return (
    <Modal
      open={open}
      title={isCreate ? '创建机器' : '轮换机器凭据'}
      busy={!!busy}
      initialFocusRef={isCreate ? nameRef : expiryRef}
      onRequestClose={onClose}
      footer={
        <>
          <Button disabled={!!busy} onClick={onClose}>
            稍后继续
          </Button>
          <Button variant="danger" disabled={!!busy} onClick={onDiscard}>
            放弃此次操作
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="machine-issuance-form"
            loading={busy === 'issuance'}
            disabled={!!busy && busy !== 'issuance'}
          >
            {pending ? '重试原请求' : isCreate ? '创建并签发凭据' : '轮换并签发凭据'}
          </Button>
        </>
      }
    >
      {error && <MachineFailure error={error} />}
      {pending && (
        <Alert tone="warning" title="操作结果尚未确认">
          重试会保留相同的请求内容和标识。若服务已签发凭据，重试只能确认记录，一次性密钥无法恢复；确认后需要另行轮换。
        </Alert>
      )}
      <form
        id="machine-issuance-form"
        className="console-form"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {isCreate ? (
          <>
            <Field label="机器名称" htmlFor="machine-name" error={fields.name} required>
              <Input
                id="machine-name"
                ref={nameRef}
                maxLength={256}
                value={name}
                disabled={disabled}
                onChange={(event) => {
                  setName(event.target.value);
                  setFields((current) => ({ ...current, name: undefined }));
                }}
              />
            </Field>
            <Field
              label="授权环境"
              htmlFor="machine-environment"
              hint="创建后，机器名称、环境和权限不可修改。需要不同授权时，请创建另一台机器。"
            >
              <Select
                id="machine-environment"
                value={selectedEnvironment}
                disabled={disabled}
                options={Object.entries(environmentLabels).map(([value, label]) => ({
                  value,
                  label,
                }))}
                onChange={(event) => setSelectedEnvironment(event.target.value as Environment)}
              />
            </Field>
            <div
              ref={permissionsRef}
              tabIndex={-1}
              className="machine-grants-field"
              aria-label="机器权限"
              role="group"
              aria-describedby={fields.permissions ? 'machine-permissions-error' : undefined}
            >
              <h3>权限</h3>
              <MachinePermissions
                value={permissions}
                disabled={disabled}
                onChange={(value) => {
                  setPermissions(value);
                  setFields((current) => ({ ...current, permissions: undefined }));
                }}
              />
              <p id="machine-permissions-error" className="evia-field-error" role="status">
                {fields.permissions}
              </p>
            </div>
          </>
        ) : (
          <p className="machine-scope">
            <strong>{intent.principal.name}</strong>
            <span>{environmentLabels[intent.principal.environment]}</span>
          </p>
        )}
        <Field
          label="凭据到期时间"
          htmlFor="machine-expiry"
          hint="使用当前设备时区，有效期为 1 分钟至 90 天。"
          error={fields.expiry}
          required
        >
          <Input
            id="machine-expiry"
            type="datetime-local"
            ref={expiryRef}
            value={expiry}
            disabled={disabled}
            onChange={(event) => {
              setExpiry(event.target.value);
              setFields((current) => ({ ...current, expiry: undefined }));
            }}
          />
        </Field>
        {!isCreate && (
          <Field
            label="旧凭据重叠时间（秒）"
            htmlFor="machine-overlap"
            hint="0 表示立即失效，最多允许旧凭据继续使用 300 秒。已撤销或已过期的凭据不会恢复。"
            error={fields.overlap}
            required
          >
            <Input
              id="machine-overlap"
              type="number"
              min={0}
              max={300}
              step={1}
              ref={overlapRef}
              value={overlap}
              disabled={disabled}
              onChange={(event) => {
                setOverlap(event.target.value);
                setFields((current) => ({ ...current, overlap: undefined }));
              }}
            />
          </Field>
        )}
      </form>
    </Modal>
  );
}
