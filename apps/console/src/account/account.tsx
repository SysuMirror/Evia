import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Session } from '@evia/api-client';
import {
  Alert,
  Button,
  DataSurface,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  PageHeading,
  useToast,
} from '@evia/design-system';
import { formatInstant } from '../governance/model';

export function AccountPage({ session }: { session: Session }) {
  const cache = useQueryClient();
  const toast = useToast();

  const profile = useQuery({
    queryKey: ['account', session.user_id],
    queryFn: ({ signal }) => api.getAccountProfile(signal),
  });

  const [displayName, setDisplayName] = useState(session.display_name);
  const [profileError, setProfileError] = useState('');

  useEffect(() => {
    if (profile.data) {
      setDisplayName(profile.data.display_name);
    }
  }, [profile.data]);

  const updateProfile = useMutation({
    mutationFn: (nextDisplayName: string) =>
      api.updateAccountProfile({ display_name: nextDisplayName }, session.csrf_token),
    onSuccess: (updated) => {
      setProfileError('');
      cache.setQueryData(['account', session.user_id], updated);
      cache.setQueryData<Session | null>(['session'], (current) =>
        current ? { ...current, display_name: updated.display_name } : current,
      );
      toast.success('显示名称已更新');
    },
    onError: (error) => {
      setProfileError(error instanceof Error ? error.message : '更新显示名称失败');
    },
  });

  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [passwordError, setPasswordError] = useState('');

  const changePassword = useMutation({
    mutationFn: (payload: { current_password: string; new_password: string }) =>
      api.changeAccountPassword(payload, session.csrf_token),
    onSuccess: (rotatedSession) => {
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
      setPasswordError('');
      cache.setQueryData(['session'], rotatedSession);
      toast.success('密码已更新，其他设备会话已退出');
    },
    onError: (error) => {
      setPasswordError(error instanceof Error ? error.message : '修改密码失败');
    },
  });

  const submitProfile = () => {
    const trimmed = displayName.trim();
    if (!trimmed || trimmed.length > 200) {
      setProfileError('显示名称不能为空且不能超过 200 个字符。');
      return;
    }
    setProfileError('');
    updateProfile.mutate(trimmed);
  };

  const submitPassword = () => {
    if (!currentPassword) {
      setPasswordError('请输入当前密码。');
      return;
    }
    if (newPassword.length < 12 || newPassword.length > 72) {
      setPasswordError('新密码长度必须为 12 到 72 位。');
      return;
    }
    if (newPassword !== confirmPassword) {
      setPasswordError('两次输入的新密码不一致。');
      return;
    }
    if (newPassword === currentPassword) {
      setPasswordError('新密码不能与当前密码相同。');
      return;
    }
    setPasswordError('');
    changePassword.mutate({
      current_password: currentPassword,
      new_password: newPassword,
    });
  };

  if (profile.isPending) return <LoadingPanel label="正在加载账号信息" />;
  if (profile.isError) {
    return (
      <ErrorPanel
        message={profile.error instanceof ApiError ? profile.error.message : '无法加载账号信息'}
        requestID={profile.error instanceof ApiError ? profile.error.requestID : undefined}
        retry={() => void profile.refetch()}
      />
    );
  }

  return (
    <div style={{ display: 'grid', gap: 24 }}>
      <PageHeading title="账号设置" />
      <DataSurface label="基本资料">
        <form
          className="console-form"
          style={{ maxWidth: 520 }}
          onSubmit={(event) => {
            event.preventDefault();
            if (!updateProfile.isPending) submitProfile();
          }}
        >
          <Field label="登录账号" htmlFor="account-username">
            <Input id="account-username" value={profile.data.username} disabled />
          </Field>
          <Field label="创建时间" htmlFor="account-created-at">
            <Input
              id="account-created-at"
              value={formatInstant(profile.data.created_at)}
              disabled
            />
          </Field>
          <Field label="显示名称" htmlFor="account-display-name" required>
            <Input
              id="account-display-name"
              value={displayName}
              maxLength={200}
              disabled={updateProfile.isPending}
              onChange={(event) => setDisplayName(event.target.value)}
            />
          </Field>
          {profileError && <Alert tone="error" title={profileError} />}
          <div>
            <Button
              variant="primary"
              type="submit"
              loading={updateProfile.isPending}
              disabled={displayName.trim() === profile.data.display_name}
            >
              保存资料
            </Button>
          </div>
        </form>
      </DataSurface>

      <DataSurface label="修改密码">
        <form
          className="console-form"
          style={{ maxWidth: 520 }}
          onSubmit={(event) => {
            event.preventDefault();
            if (!changePassword.isPending) submitPassword();
          }}
        >
          <Field label="当前密码" htmlFor="account-current-password" required>
            <Input
              id="account-current-password"
              type="password"
              autoComplete="current-password"
              maxLength={72}
              value={currentPassword}
              disabled={changePassword.isPending}
              onChange={(event) => setCurrentPassword(event.target.value)}
            />
          </Field>
          <Field
            label="新密码"
            htmlFor="account-new-password"
            hint="密码长度需在 12 到 72 位之间。修改成功后将自动轮换当前会话并退出其他设备。"
            required
          >
            <Input
              id="account-new-password"
              type="password"
              autoComplete="new-password"
              maxLength={72}
              value={newPassword}
              disabled={changePassword.isPending}
              onChange={(event) => setNewPassword(event.target.value)}
            />
          </Field>
          <Field label="确认新密码" htmlFor="account-confirm-password" required>
            <Input
              id="account-confirm-password"
              type="password"
              autoComplete="new-password"
              maxLength={72}
              value={confirmPassword}
              disabled={changePassword.isPending}
              onChange={(event) => setConfirmPassword(event.target.value)}
            />
          </Field>
          {passwordError && <Alert tone="error" title={passwordError} />}
          <div>
            <Button variant="primary" type="submit" loading={changePassword.isPending}>
              更新密码
            </Button>
          </div>
        </form>
      </DataSurface>
    </div>
  );
}
