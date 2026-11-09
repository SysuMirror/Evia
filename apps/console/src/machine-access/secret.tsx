import { useState } from 'react';
import type { MachinePrincipal } from '@evia/api-client';
import { Alert, Button, Checkbox, CopyButton, Field, Input, Modal } from '@evia/design-system';
import { environmentLabels } from './model';

export function MachineSecret({
  issued,
  saved,
  onSaved,
  onClose,
}: {
  issued: { principal: MachinePrincipal; secret: string };
  saved: boolean;
  onSaved: (saved: boolean) => void;
  onClose: () => void;
}) {
  const [visible, setVisible] = useState(false);
  return (
    <Modal
      open
      title="保存机器密钥"
      onRequestClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
          完成
        </Button>
      }
    >
      <Alert tone="warning" title="请立即安全保存密钥">
        此密钥仅在本次签发时提供，关闭后无法重新查看。请保存在服务端密钥管理工具中。
      </Alert>
      <p className="machine-scope">
        <strong>{issued.principal.name}</strong>
        <span>{environmentLabels[issued.principal.environment]}</span>
      </p>
      <div className="console-form">
        <Field
          label="机器密钥"
          htmlFor="machine-issued-secret"
          hint="不要放入网页代码、URL 或日志。"
        >
          <Input
            id="machine-issued-secret"
            type={visible ? 'text' : 'password'}
            autoComplete="off"
            spellCheck={false}
            value={issued.secret}
            readOnly
          />
          <div className="inline-actions">
            <Button onClick={() => setVisible((value) => !value)}>
              {visible ? '隐藏密钥' : '显示密钥'}
            </Button>
            <CopyButton value={issued.secret} label="复制机器密钥" />
          </div>
        </Field>
        <Checkbox checked={saved} onChange={(event) => onSaved(event.target.checked)}>
          我已安全保存机器密钥
        </Checkbox>
      </div>
    </Modal>
  );
}
