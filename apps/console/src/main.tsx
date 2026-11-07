import React, { lazy, Suspense, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import {
  MutationCache,
  QueryCache,
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { api, ApiError, type Session } from '@evia/api-client';
import {
  Button,
  DesignProvider,
  ErrorPanel,
  Form,
  Input,
  LoadingPanel,
  WorkspaceShell,
} from '@evia/design-system';
const ProjectsPage = lazy(() =>
  import('./projects').then((module) => ({ default: module.ProjectsPage })),
);

const expire = (error: Error) => {
  if (error instanceof ApiError && error.status === 401) {
    client.removeQueries({ queryKey: ['projects'] });
    client.setQueryData(['session'], null);
  }
};
const client = new QueryClient({
  queryCache: new QueryCache({ onError: expire }),
  mutationCache: new MutationCache({ onError: expire }),
  defaultOptions: { queries: { retry: false, staleTime: 30_000 }, mutations: { retry: false } },
});

function Failure({ error, retry }: { error: Error; retry?: () => void }) {
  return (
    <ErrorPanel
      message={error.message}
      requestID={error instanceof ApiError ? error.requestID : undefined}
      retry={retry}
    />
  );
}
function LoginPage() {
  const cache = useQueryClient();
  const login = useMutation({
    mutationFn: api.login,
    onSuccess: (session) => {
      cache.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
      cache.setQueryData(['session'], session);
    },
  });
  return (
    <div className="login-page">
      <section className="login-intro">
        <a className="brand" href="/login">
          <span className="brand-mark" aria-hidden="true">
            e
          </span>
          <span>
            Evia <small>映途</small>
          </span>
        </a>
        <div>
          <h1>
            从每一次交互，
            <br />
            理解业务的发生。
          </h1>
          <p>在自己的基础设施中，建立统一的事件定义与分析空间。</p>
        </div>
        <small>SELF-HOSTED EVENT ANALYTICS</small>
      </section>
      <main className="login-form-area">
        <div className="login-form">
          <h1>登录工作空间</h1>
          <p className="muted">使用平台管理员为你创建的账号。</p>
          {login.error && <Failure error={login.error} />}
          <Form
            layout="vertical"
            requiredMark={false}
            onFinish={(values) => login.mutate(values)}
            disabled={login.isPending}
          >
            <Form.Item
              name="username"
              label="账号"
              rules={[
                { required: true, message: '请输入账号' },
                { pattern: /^[a-zA-Z0-9_.@-]{1,100}$/, message: '账号仅支持字母、数字和 . _ @ -' },
              ]}
            >
              <Input autoComplete="username" autoFocus maxLength={100} />
            </Form.Item>
            <Form.Item
              name="password"
              label="密码"
              rules={[{ required: true, message: '请输入密码' }]}
            >
              <Input.Password autoComplete="current-password" maxLength={72} />
            </Form.Item>
            <Button type="primary" htmlType="submit" block loading={login.isPending}>
              登录
            </Button>
          </Form>
          <p className="login-footer">无法登录时，请联系你的平台管理员。</p>
        </div>
      </main>
    </div>
  );
}
function Workspace({ session }: { session: Session }) {
  const cache = useQueryClient();
  const [logoutError, setLogoutError] = useState<Error>();
  const logout = useMutation({
    mutationFn: () => api.logout(session.csrf_token),
    onSuccess: () => {
      cache.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
      cache.setQueryData(['session'], null);
    },
    onError: setLogoutError,
  });
  return (
    <WorkspaceShell
      account={session.display_name}
      busy={logout.isPending}
      onLogout={() => {
        setLogoutError(undefined);
        logout.mutate();
      }}
    >
      {logoutError && (
        <div className="inline-notice">
          <Failure error={logoutError} />
        </div>
      )}
      <Suspense fallback={<LoadingPanel />}>
        <Routes>
          <Route path="/projects" element={<ProjectsPage session={session} />} />
          <Route path="/" element={<Navigate to="/projects" replace />} />
          <Route path="/login" element={<Navigate to="/projects" replace />} />
          <Route
            path="*"
            element={
              <ErrorPanel message="页面不存在" retry={() => window.location.assign('/projects')} />
            }
          />
        </Routes>
      </Suspense>
    </WorkspaceShell>
  );
}
function Root() {
  const session = useQuery({
    queryKey: ['session'],
    queryFn: async ({ signal }) => {
      try {
        return await api.session(signal);
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) return null;
        throw error;
      }
    },
  });
  if (session.isPending) return <LoadingPanel label="正在连接工作空间" />;
  if (session.isError)
    return (
      <div style={{ padding: 32 }}>
        <Failure error={session.error} retry={() => void session.refetch()} />
      </div>
    );
  if (!session.data)
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  return <Workspace session={session.data} />;
}
createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <DesignProvider>
      <QueryClientProvider client={client}>
        <BrowserRouter>
          <Root />
        </BrowserRouter>
      </QueryClientProvider>
    </DesignProvider>
  </React.StrictMode>,
);
