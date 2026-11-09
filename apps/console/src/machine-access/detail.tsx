import { useEffect, useState } from 'react';
import type { MachineCredential, MachinePrincipalDetail } from '@evia/api-client';
import {
  Badge,
  Button,
  CopyButton,
  DataTable,
  EmptyPanel,
  type DataTableColumn,
} from '@evia/design-system';
import { credentialState, dateLabel, environmentLabels } from './model';
import { MachinePermissions } from './permissions';

export function MachineDetail({
  detail,
  busy,
  onRotate,
  onRevoke,
  onRevokeCredential,
}: {
  detail: MachinePrincipalDetail;
  busy: boolean;
  onRotate: () => void;
  onRevoke: () => void;
  onRevokeCredential: (credential: MachineCredential) => void;
}) {
  const principal = detail.principal;
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const next = Math.min(
      ...detail.credentials
        .filter((row) => !row.revoked_at && Date.parse(row.expires_at) > now)
        .map((row) => Date.parse(row.expires_at)),
    );
    if (!Number.isFinite(next)) return;
    const timer = window.setTimeout(
      () => setNow(Date.now()),
      Math.min(next - Date.now() + 25, 60_000),
    );
    return () => window.clearTimeout(timer);
  }, [detail.credentials, now]);
  const columns: DataTableColumn<MachineCredential>[] = [
    {
      key: 'credential',
      header: '凭据',
      render: (row) => (
        <div className="machine-credential-id">
          <code>{row.id.slice(0, 8)}</code>
          <CopyButton value={row.id} label={`复制凭据 ${row.id.slice(0, 8)} 的标识`} />
        </div>
      ),
    },
    {
      key: 'state',
      header: '状态',
      render: (row) => {
        const state = principal.revoked_at
          ? { label: '机器已撤销', tone: 'neutral' as const }
          : credentialState(row, now);
        return <Badge tone={state.tone}>{state.label}</Badge>;
      },
    },
    { key: 'created', header: '签发时间', render: (row) => dateLabel(row.created_at) },
    { key: 'expiry', header: '到期时间', render: (row) => dateLabel(row.expires_at) },
    { key: 'revoked', header: '撤销时间', render: (row) => dateLabel(row.revoked_at) },
    {
      key: 'actions',
      header: '操作',
      render: (row) => (
        <Button
          variant="link"
          disabled={busy || !!principal.revoked_at || credentialState(row, now).label !== '有效'}
          onClick={() => onRevokeCredential(row)}
          aria-label={`撤销凭据 ${row.id.slice(0, 8)}`}
        >
          撤销
        </Button>
      ),
    },
  ];
  return (
    <div className="machine-detail">
      <div className="machine-detail-heading">
        <div className="machine-scope">
          <Badge>{environmentLabels[principal.environment]}</Badge>
          <Badge tone={principal.revoked_at ? 'neutral' : 'success'}>
            {principal.revoked_at ? '已撤销' : '启用'}
          </Badge>
        </div>
        <div className="inline-actions">
          <Button
            onClick={onRotate}
            disabled={busy || !!principal.revoked_at || detail.credentials.length >= 128}
          >
            轮换凭据
          </Button>
          <Button variant="danger" onClick={onRevoke} disabled={busy || !!principal.revoked_at}>
            撤销机器
          </Button>
        </div>
      </div>
      {detail.credentials.length >= 128 && (
        <p className="evia-field-hint">已达到此机器的 128 条凭据签发上限，请创建另一台机器。</p>
      )}
      <dl className="machine-facts">
        <div>
          <dt>创建时间</dt>
          <dd>{dateLabel(principal.created_at)}</dd>
        </div>
        <div>
          <dt>撤销时间</dt>
          <dd>{dateLabel(principal.revoked_at)}</dd>
        </div>
        <div className="machine-identity">
          <dt>机器标识</dt>
          <dd>
            <code>{principal.id}</code>
            <CopyButton value={principal.id} label="复制机器标识" />
          </dd>
        </div>
      </dl>
      <section aria-label="已授予的权限">
        <h3>已授予的权限</h3>
        <MachinePermissions value={principal.permissions} />
        <p className="evia-field-hint">授权创建后不可修改，所有凭据共用这些权限和环境。</p>
      </section>
      <section aria-label="凭据记录">
        <h3>凭据记录</h3>
        <DataTable
          caption="机器凭据记录"
          rows={detail.credentials}
          columns={columns}
          rowKey={(row) => row.id}
          minWidth={780}
          empty={<EmptyPanel description="尚无凭据记录" />}
        />
      </section>
    </div>
  );
}
