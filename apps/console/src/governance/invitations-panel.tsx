import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Project,
  type ProjectInvitation,
  type ProjectInvitationIssued,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  Modal,
  PaginationBar,
  Select,
  Toolbar,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { formatInstant, isGovernanceRole, roleLabel, roleOptions } from './model';
import type { GovernancePerform } from './use-governance-request';

interface InvitationsPanelProps {
  project: Project;
  session: Session;
  perform: GovernancePerform;
  busy: string;
}

const expiryOptions = [
  { value: '24', label: '24 小时' },
  { value: '168', label: '7 天（默认）' },
  { value: '720', label: '30 天' },
];

const statusLabel: Record<ProjectInvitation['status'], string> = {
  pending: '待接受',
  accepted: '已接受',
  revoked: '已撤销',
  expired: '已过期',
};

const statusTone: Record<ProjectInvitation['status'], 'success' | 'warning' | 'neutral'> = {
  pending: 'warning',
  accepted: 'success',
  revoked: 'neutral',
  expired: 'neutral',
};

function buildInviteURL(token: string): string {
  return `${window.location.origin}/invite?token=${encodeURIComponent(token)}`;
}

export function InvitationsPanel({ project, session, perform, busy }: InvitationsPanelProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const queryPrefix = ['governance', project.id, session.user_id, 'invitations'] as const;
  const [cursors, setCursors] = useState<string[]>([]);
  const [role, setRole] = useState('viewer');
  const [note, setNote] = useState('');
  const [expiresInHours, setExpiresInHours] = useState('168');
  const [notice, setNotice] = useState('');
  const [issued, setIssued] = useState<ProjectInvitationIssued>();
  const [revokeTarget, setRevokeTarget] = useState<ProjectInvitation>();

  const invitations = useQuery({
    queryKey: [...queryPrefix, cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listProjectInvitations(project.id, { limit: 20, cursor: cursors.at(-1) }, signal),
  });

  const refresh = () => cache.invalidateQueries({ queryKey: queryPrefix });

  const createInvitation = () => {
    if (!isGovernanceRole(role)) {
      setNotice('请选择成员角色。');
      return;
    }
    const hours = Number(expiresInHours);
    setNotice('');
    void perform(
      'invitations.create',
      (signal) =>
        api.createProjectInvitation(
          project.id,
          {
            role,
            note: note.trim() || undefined,
            expires_in_hours: Number.isFinite(hours) ? hours : 168,
          },
          session.csrf_token,
          signal,
        ),
      (result) => {
        setIssued(result);
        setNote('');
        setRole('viewer');
        setCursors([]);
        refresh();
        toast.success('邀请链接已生成');
      },
      (error) => {
        setNotice(error.message);
        return undefined;
      },
    );
  };

  const revokeInvitation = (target: ProjectInvitation) => {
    setNotice('');
    void perform(
      'invitations.revoke',
      (signal) => api.revokeProjectInvitation(project.id, target.id, session.csrf_token, signal),
      () => {
        setRevokeTarget(undefined);
        refresh();
        toast.success('邀请链接已撤销');
      },
      (error) => {
        setNotice(error.message);
        return undefined;
      },
    );
  };

  const copyInviteLink = async (token: string) => {
    const url = buildInviteURL(token);
    try {
      await navigator.clipboard.writeText(url);
      toast.success('已复制邀请链接');
    } catch {
      toast.error('无法自动复制，请手动选中上方链接复制');
    }
  };

  const columns: DataTableColumn<ProjectInvitation>[] = [
    {
      key: 'role',
      header: '赋予角色',
      render: (row) => <Badge tone="neutral">{roleLabel(row.role)}</Badge>,
    },
    {
      key: 'note',
      header: '备注',
      render: (row) => row.note || '—',
    },
    {
      key: 'status',
      header: '状态',
      render: (row) => <Badge tone={statusTone[row.status]}>{statusLabel[row.status]}</Badge>,
    },
    {
      key: 'created_by',
      header: '创建人',
      render: (row) => row.created_by_display_name,
    },
    {
      key: 'expires_at',
      header: '过期时间',
      render: (row) => formatInstant(row.expires_at),
    },
    {
      key: 'accepted',
      header: '接受账号',
      render: (row) =>
        row.accepted_username
          ? `${row.accepted_username}${row.accepted_at ? `（${formatInstant(row.accepted_at)}）` : ''}`
          : '—',
    },
    {
      key: 'actions',
      header: '操作',
      kind: 'actions',
      align: 'right',
      render: (row) =>
        row.status === 'pending' ? (
          <Button
            variant="danger"
            size="small"
            disabled={!!busy}
            onClick={() => {
              setNotice('');
              setRevokeTarget(row);
            }}
          >
            撤销
          </Button>
        ) : (
          '—'
        ),
    },
  ];

  const issuedURL = issued ? buildInviteURL(issued.invite_token) : '';

  return (
    <DataSurface label="项目邀请链接">
      <Toolbar label="生成邀请链接">
        <div className="governance-fields governance-filters">
          <Field label="赋予角色" htmlFor="governance-invite-role">
            <Select
              id="governance-invite-role"
              value={role}
              options={roleOptions}
              disabled={!!busy}
              onChange={(event) => setRole(event.target.value)}
            />
          </Field>
          <Field label="有效期" htmlFor="governance-invite-expiry">
            <Select
              id="governance-invite-expiry"
              value={expiresInHours}
              options={expiryOptions}
              disabled={!!busy}
              onChange={(event) => setExpiresInHours(event.target.value)}
            />
          </Field>
          <Field label="备注（可选）" htmlFor="governance-invite-note">
            <Input
              id="governance-invite-note"
              value={note}
              placeholder="例如：前端同学 / 数据分析师"
              maxLength={200}
              disabled={!!busy}
              onChange={(event) => setNote(event.target.value)}
            />
          </Field>
        </div>
      </Toolbar>
      <div className="inline-actions governance-add-action">
        <Button
          variant="primary"
          loading={busy === 'invitations.create'}
          onClick={createInvitation}
        >
          生成一次性邀请链接
        </Button>
        <span className="evia-field-hint">
          受邀者可通过链接自助注册新账号，或使用已有账号直接加入本项目。
        </span>
      </div>

      {issued && (
        <div className="inline-notice">
          <Alert
            tone="success"
            title="邀请链接已生成（仅展示一次，请立即复制发送）"
            action={
              <div style={{ display: 'flex', gap: 8 }}>
                <Button size="small" onClick={() => void copyInviteLink(issued.invite_token)}>
                  复制邀请链接
                </Button>
                <Button size="small" variant="ghost" onClick={() => setIssued(undefined)}>
                  关闭
                </Button>
              </div>
            }
          >
            <div style={{ marginTop: 8 }}>
              <Input
                aria-label="一次性邀请链接"
                value={issuedURL}
                readOnly
                onFocus={(event) => event.currentTarget.select()}
              />
            </div>
          </Alert>
        </div>
      )}

      {notice && (
        <div className="inline-notice">
          <Alert tone="error" title={notice} />
        </div>
      )}

      {invitations.isPending ? (
        <EmptyPanel description="正在加载邀请记录…" />
      ) : invitations.isError ? (
        <Alert
          tone="error"
          title="无法读取邀请记录"
          action={<Button onClick={() => void invitations.refetch()}>重试</Button>}
        >
          {invitations.error instanceof ApiError ? invitations.error.message : '请稍后再试。'}
        </Alert>
      ) : (
        <>
          <DataTable
            caption="项目邀请记录"
            columns={columns}
            rows={invitations.data.items}
            rowKey={(row) => row.id}
            minWidth={760}
            loading={invitations.isFetching}
            empty={<EmptyPanel description="还没有生成过邀请链接。" />}
          />
          <PaginationBar
            page={cursors.length + 1}
            count={invitations.data.items.length}
            hasNext={!!invitations.data.next_cursor}
            busy={invitations.isFetching || !!busy}
            onPrevious={() => setCursors((current) => current.slice(0, -1))}
            onNext={() => {
              if (invitations.data?.next_cursor)
                setCursors((current) => [...current, invitations.data!.next_cursor!]);
            }}
          />
        </>
      )}

      <Modal
        open={!!revokeTarget}
        title="撤销邀请链接"
        busy={busy === 'invitations.revoke'}
        onRequestClose={() => {
          if (!busy) setRevokeTarget(undefined);
        }}
        footer={
          <>
            <Button disabled={!!busy} onClick={() => setRevokeTarget(undefined)}>
              取消
            </Button>
            <Button
              variant="danger"
              loading={busy === 'invitations.revoke'}
              onClick={() => revokeTarget && revokeInvitation(revokeTarget)}
            >
              确认撤销
            </Button>
          </>
        }
      >
        {revokeTarget && (
          <p>
            确认撤销此条 <strong>{roleLabel(revokeTarget.role)}</strong>{' '}
            邀请链接？撤销后该链接立即失效，无法再用于注册或加入项目。
          </p>
        )}
      </Modal>
    </DataSurface>
  );
}
