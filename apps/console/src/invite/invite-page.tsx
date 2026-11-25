import { useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Session } from '@evia/api-client';
import {
  Alert,
  AuthLayout,
  Badge,
  Button,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
} from '@evia/design-system';
import { formatInstant, roleLabel } from '../governance/model';

export function InvitePage({ session }: { session: Session | null }) {
  const cache = useQueryClient();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const token = (params.get('token') ?? '').trim();

  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [formError, setFormError] = useState('');

  const preview = useQuery({
    queryKey: ['invitation-preview', token],
    queryFn: ({ signal }) => api.previewInvitation({ token }, signal),
    enabled: token.length >= 16,
  });

  const accept = useMutation({
    mutationFn: () => {
      if (session) {
        return api.acceptInvitation({ token }, session.csrf_token);
      }
      return api.acceptInvitation({
        token,
        username: username.trim(),
        display_name: displayName.trim(),
        password,
      });
    },
    onSuccess: (result) => {
      cache.setQueryData(['session'], result.session);
      void cache.invalidateQueries({ queryKey: ['projects'] });
      void navigate(
        `/projects/${encodeURIComponent(result.project_id)}/overview?environment=development`,
        {
          replace: true,
        },
      );
    },
    onError: (error) => {
      setFormError(error instanceof Error ? error.message : '接受邀请失败');
    },
  });

  const submitNewAccount = () => {
    const cleanUsername = username.trim();
    const cleanDisplayName = displayName.trim();
    if (!/^[a-zA-Z0-9_.@-]{1,100}$/.test(cleanUsername)) {
      setFormError('账号仅支持 1 到 100 位字母、数字和 . _ @ -。');
      return;
    }
    if (!cleanDisplayName || cleanDisplayName.length > 200) {
      setFormError('请输入 1 到 200 字符的显示名称。');
      return;
    }
    if (password.length < 12 || password.length > 72) {
      setFormError('密码长度必须为 12 到 72 位。');
      return;
    }
    if (password !== confirmPassword) {
      setFormError('两次输入的密码不一致。');
      return;
    }
    setFormError('');
    accept.mutate();
  };

  if (!token || token.length < 16) {
    return (
      <AuthLayout onNavigate={(href) => void navigate(href)}>
        <h1>项目邀请</h1>
        <ErrorPanel message="邀请链接缺少有效凭证，请联系项目所有者重新发送。" />
        <p className="auth-help">
          <Link to={session ? '/projects' : '/login'}>{session ? '返回项目列表' : '前往登录'}</Link>
        </p>
      </AuthLayout>
    );
  }

  if (preview.isPending) {
    return (
      <AuthLayout onNavigate={(href) => void navigate(href)}>
        <h1>项目邀请</h1>
        <LoadingPanel label="正在校验邀请链接" />
      </AuthLayout>
    );
  }

  if (preview.isError) {
    return (
      <AuthLayout onNavigate={(href) => void navigate(href)}>
        <h1>项目邀请</h1>
        <ErrorPanel
          message={
            preview.error instanceof ApiError ? preview.error.message : '邀请链接无效或已失效'
          }
          requestID={preview.error instanceof ApiError ? preview.error.requestID : undefined}
        />
        <p className="auth-help">
          <Link to={session ? '/projects' : '/login'}>{session ? '返回项目列表' : '前往登录'}</Link>
        </p>
      </AuthLayout>
    );
  }

  const returnPath = `/invite?token=${encodeURIComponent(token)}`;

  return (
    <AuthLayout>
      <h1>加入项目</h1>
      <div style={{ display: 'grid', gap: 8, marginBottom: 16 }}>
        <div>
          项目：<strong>{preview.data.project_name}</strong>
        </div>
        <div>
          赋予角色：<Badge tone="success">{roleLabel(preview.data.role)}</Badge>
        </div>
        {preview.data.note && <div>备注：{preview.data.note}</div>}
        <div className="evia-field-hint">
          链接有效期至：{formatInstant(preview.data.expires_at)}
        </div>
      </div>

      {formError && (
        <div style={{ marginBottom: 16 }}>
          <Alert tone="error" title={formError} />
        </div>
      )}

      {session ? (
        <div className="console-form">
          <p>
            当前已登录为 <strong>{session.display_name}</strong>，确认接受邀请并以{' '}
            <strong>{roleLabel(preview.data.role)}</strong> 身份加入该项目？
          </p>
          <Button
            variant="primary"
            className="auth-submit"
            loading={accept.isPending}
            onClick={() => {
              setFormError('');
              accept.mutate();
            }}
          >
            接受邀请并加入项目
          </Button>
          <p className="auth-help">
            <Link to="/projects">暂不加入，返回项目列表</Link>
          </p>
        </div>
      ) : (
        <>
          <form
            className="console-form"
            onSubmit={(event) => {
              event.preventDefault();
              if (!accept.isPending) submitNewAccount();
            }}
          >
            <Field label="设置登录账号" htmlFor="invite-username" required>
              <Input
                id="invite-username"
                name="username"
                autoComplete="username"
                autoFocus
                maxLength={100}
                pattern={'[a-zA-Z0-9_.@\\-]{1,100}'}
                required
                value={username}
                disabled={accept.isPending}
                onChange={(event) => setUsername(event.target.value)}
              />
            </Field>
            <Field label="显示名称" htmlFor="invite-display-name" required>
              <Input
                id="invite-display-name"
                name="display_name"
                maxLength={200}
                required
                value={displayName}
                disabled={accept.isPending}
                onChange={(event) => setDisplayName(event.target.value)}
              />
            </Field>
            <Field
              label="设置密码"
              htmlFor="invite-password"
              hint="密码长度需为 12 到 72 位。"
              required
            >
              <Input
                id="invite-password"
                name="password"
                type="password"
                autoComplete="new-password"
                maxLength={72}
                required
                value={password}
                disabled={accept.isPending}
                onChange={(event) => setPassword(event.target.value)}
              />
            </Field>
            <Field label="确认密码" htmlFor="invite-confirm-password" required>
              <Input
                id="invite-confirm-password"
                name="confirm_password"
                type="password"
                autoComplete="new-password"
                maxLength={72}
                required
                value={confirmPassword}
                disabled={accept.isPending}
                onChange={(event) => setConfirmPassword(event.target.value)}
              />
            </Field>
            <Button
              variant="primary"
              type="submit"
              className="auth-submit"
              loading={accept.isPending}
            >
              注册并加入项目
            </Button>
          </form>
          <p className="auth-help">
            已有 Evia 账号？{' '}
            <Link to={`/login?next=${encodeURIComponent(returnPath)}`}>登录后直接加入</Link>
          </p>
        </>
      )}
    </AuthLayout>
  );
}
