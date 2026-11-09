import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Project,
  type ProjectMember,
  type ProjectMemberResult,
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
import {
  canAssignRole,
  isLastEnabledOwner,
  isRevisionConflict,
  isUncertainWrite,
  isGovernanceRole,
  roleLabel,
  roleOptions,
} from './model';
import type { GovernancePerform } from './use-governance-request';

interface MembersPanelProps {
  project: Project;
  session: Session;
  perform: GovernancePerform;
  busy: string;
}

interface PendingMemberAction {
  label: string;
  retry: () => void;
}

export function MembersPanel({ project, session, perform, busy }: MembersPanelProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const queryPrefix = ['governance', project.id, session.user_id] as const;
  const [cursors, setCursors] = useState<string[]>([]);
  const [username, setUsername] = useState('');
  const [role, setRole] = useState('viewer');
  const [formError, setFormError] = useState('');
  const [notice, setNotice] = useState('');
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [removeTarget, setRemoveTarget] = useState<ProjectMember>();
  const [pending, setPending] = useState<PendingMemberAction>();

  const members = useQuery({
    queryKey: [...queryPrefix, 'members', cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listMembers(project.id, { limit: 20, cursor: cursors.at(-1) }, signal),
  });

  const revision = members.data?.revision ?? 1;
  const refresh = () => cache.invalidateQueries({ queryKey: queryPrefix });
  const handleConflict = () => {
    setCursors([]);
    setDrafts({});
    setPending(undefined);
    setNotice('成员集合已被其他人修改，已回到第一页，请核对后重试。');
    refresh();
  };
  const settleError = (error: Error, retry: () => void, label: string): boolean => {
    if (isRevisionConflict(error)) {
      handleConflict();
      return true;
    }
    if (isUncertainWrite(error)) {
      setPending({ label, retry });
      setNotice('提交结果未知，已保留原请求与幂等键，可重试确认。');
      return true;
    }
    setNotice(error.message);
    return false;
  };

  const addMember = () => {
    const nextUsername = username.trim();
    if (!/^[a-zA-Z0-9_.@-]{1,100}$/.test(nextUsername)) {
      setFormError('账号仅支持字母、数字和 . _ @ -，且必须是已存在的启用账号。');
      return;
    }
    if (!isGovernanceRole(role)) {
      setFormError('请选择成员角色。');
      return;
    }
    setFormError('');
    setNotice('');
    const body = { username: nextUsername, role };
    const key = crypto.randomUUID();
    const run = (idempotencyKey: string) =>
      void perform(
        'members.add',
        (signal) =>
          api.addMember(project.id, revision, body, idempotencyKey, session.csrf_token, signal),
        () => {
          setUsername('');
          setRole('viewer');
          setPending(undefined);
          setNotice('');
          refresh();
          toast.success('成员已添加');
        },
        (error) => settleError(error, () => run(idempotencyKey), '重试添加成员') || undefined,
      );
    run(key);
  };

  const changeRole = (member: ProjectMember, nextRole: string) => {
    if (
      !isGovernanceRole(nextRole) ||
      nextRole === member.role ||
      !canAssignRole(members.data?.items ?? [], member.user_id, nextRole)
    )
      return;
    setNotice('');
    const body = { role: nextRole };
    const key = crypto.randomUUID();
    const run = (idempotencyKey: string) =>
      void perform(
        'members.role',
        (signal) =>
          api.updateMember(
            project.id,
            member.user_id,
            revision,
            body,
            idempotencyKey,
            session.csrf_token,
            signal,
          ),
        (result: ProjectMemberResult) => {
          setDrafts((current) => {
            const copy = { ...current };
            delete copy[member.user_id];
            return copy;
          });
          setPending(undefined);
          setNotice('');
          refresh();
          toast.success(`已将 ${result.member.username} 调整为${roleLabel(result.member.role)}`);
        },
        (error) =>
          settleError(error, () => run(idempotencyKey), `重试调整 ${member.username} 的角色`) ||
          undefined,
      );
    run(key);
  };

  const removeMember = (member: ProjectMember) => {
    setNotice('');
    const key = crypto.randomUUID();
    const run = (idempotencyKey: string) =>
      void perform(
        'members.remove',
        (signal) =>
          api.removeMember(
            project.id,
            member.user_id,
            revision,
            idempotencyKey,
            session.csrf_token,
            signal,
          ),
        () => {
          setRemoveTarget(undefined);
          setPending(undefined);
          setNotice('');
          refresh();
          toast.success(`已移除成员 ${member.username}`);
        },
        (error) =>
          settleError(error, () => run(idempotencyKey), `重试移除 ${member.username}`) || undefined,
      );
    run(key);
  };

  const columns: DataTableColumn<ProjectMember>[] = [
    {
      key: 'username',
      header: '账号',
      render: (row) => (
        <span>
          {row.username}
          {row.disabled && <Badge tone="neutral"> 已停用</Badge>}
        </span>
      ),
    },
    { key: 'display_name', header: '显示名称', render: (row) => row.display_name },
    {
      key: 'role',
      header: '角色',
      render: (row) => {
        if (isLastEnabledOwner(members.data?.items ?? [], row.user_id)) {
          return (
            <span>
              <Badge tone="success">{roleLabel(row.role)}</Badge>
              <span className="evia-field-hint"> 最后一名所有者，不可降级或移除</span>
            </span>
          );
        }
        const selected = drafts[row.user_id] ?? row.role;
        return (
          <span className="governance-role-cell">
            <Select
              aria-label={`${row.username} 的角色`}
              value={selected}
              options={roleOptions}
              disabled={!!busy}
              onChange={(event) =>
                setDrafts((current) => ({ ...current, [row.user_id]: event.target.value }))
              }
            />
            <Button
              size="small"
              disabled={selected === row.role || !!busy}
              onClick={() => changeRole(row, selected)}
            >
              应用
            </Button>
          </span>
        );
      },
    },
    {
      key: 'actions',
      header: '操作',
      align: 'right',
      render: (row) => {
        const lastOwner = isLastEnabledOwner(members.data?.items ?? [], row.user_id);
        return (
          <Button
            variant="danger"
            size="small"
            disabled={lastOwner || !!busy}
            onClick={() => {
              setNotice('');
              setRemoveTarget(row);
            }}
          >
            移除
          </Button>
        );
      },
    },
  ];

  return (
    <DataSurface label="项目成员">
      <Toolbar label="添加成员">
        <div className="governance-fields">
          <Field label="账号名称" htmlFor="governance-member-username" error={formError}>
            <Input
              id="governance-member-username"
              value={username}
              placeholder="已存在的账号名称"
              maxLength={100}
              autoComplete="off"
              disabled={!!busy}
              onChange={(event) => setUsername(event.target.value)}
            />
          </Field>
          <Field label="角色" htmlFor="governance-member-role">
            <Select
              id="governance-member-role"
              value={role}
              options={roleOptions}
              disabled={!!busy}
              onChange={(event) => setRole(event.target.value)}
            />
          </Field>
        </div>
      </Toolbar>
      <div className="inline-actions governance-add-action">
        <Button variant="primary" loading={busy === 'members.add'} onClick={addMember}>
          添加成员
        </Button>
        <span className="evia-field-hint">添加只接受已存在且启用的账号，不提供全局账号搜索。</span>
      </div>
      {notice && (
        <div className="inline-notice">
          <Alert
            tone={pending ? 'warning' : 'error'}
            title={notice}
            action={
              pending ? (
                <Button loading={!!busy} onClick={pending.retry}>
                  {pending.label}
                </Button>
              ) : undefined
            }
          />
        </div>
      )}
      {members.isPending ? (
        <EmptyPanel description="正在加载成员…" />
      ) : members.isError ? (
        <Alert
          tone="error"
          title="无法读取成员列表"
          action={<Button onClick={() => void members.refetch()}>重试</Button>}
        >
          {members.error instanceof ApiError ? members.error.message : '请稍后再试。'}
        </Alert>
      ) : (
        <>
          <DataTable
            caption="项目成员列表"
            columns={columns}
            rows={members.data.items}
            rowKey={(row) => row.user_id}
            minWidth={640}
            loading={members.isFetching}
            empty={<EmptyPanel description="当前只有你一名成员。" />}
          />
          <PaginationBar
            page={cursors.length + 1}
            count={members.data.items.length}
            hasNext={!!members.data.next_cursor}
            busy={members.isFetching || !!busy}
            onPrevious={() => setCursors((current) => current.slice(0, -1))}
            onNext={() => {
              if (members.data?.next_cursor)
                setCursors((current) => [...current, members.data!.next_cursor!]);
            }}
          />
        </>
      )}
      <Modal
        open={!!removeTarget}
        title="移除项目成员"
        busy={busy === 'members.remove'}
        onRequestClose={() => {
          if (!busy) setRemoveTarget(undefined);
        }}
        footer={
          <>
            <Button disabled={!!busy} onClick={() => setRemoveTarget(undefined)}>
              取消
            </Button>
            <Button
              variant="danger"
              loading={busy === 'members.remove'}
              onClick={() => removeTarget && removeMember(removeTarget)}
            >
              确认移除
            </Button>
          </>
        }
      >
        {removeTarget && (
          <p>
            确认将 <strong>{removeTarget.username}</strong>（{roleLabel(removeTarget.role)}
            ）移出项目？移除后该账号无法继续读取或操作本项目，其历史结果不会被删除。
          </p>
        )}
      </Modal>
    </DataSurface>
  );
}
