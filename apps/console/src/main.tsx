import React, { lazy, Suspense, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  createBrowserRouter,
  Navigate,
  Route,
  RouterProvider,
  Routes,
  useNavigate,
} from 'react-router-dom';
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
  AuthLayout,
  Button,
  DesignProvider,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  WorkspaceShell,
} from '@evia/design-system';
import { DraftNavigationProvider, useDraftNavigation } from './draft-navigation';
const ProjectsPage = lazy(() =>
  import('./projects').then((module) => ({ default: module.ProjectsPage })),
);

const expire = (error: Error) => {
  if (error instanceof ApiError && error.status === 401) {
    client.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
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
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [errors, setErrors] = useState<{ username?: string; password?: string }>({});
  const login = useMutation({
    mutationFn: api.login,
    onSuccess: (session) => {
      cache.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
      cache.setQueryData(['session'], session);
    },
  });
  return (
    <AuthLayout>
      <h1>登录</h1>
      {login.error && <Failure error={login.error} />}
      <form
        className="console-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (!login.isPending) login.mutate({ username, password });
        }}
      >
        <Field label="账号" htmlFor="login-username" error={errors.username} required>
          <Input
            id="login-username"
            name="username"
            autoComplete="username"
            autoFocus
            maxLength={100}
            pattern={'[a-zA-Z0-9_.@\\-]{1,100}'}
            required
            value={username}
            disabled={login.isPending}
            onChange={(event) => {
              setUsername(event.target.value);
              setErrors((current) => ({ ...current, username: undefined }));
            }}
            onInvalid={(event) => {
              const message = event.currentTarget.validity.valueMissing
                ? '请输入账号'
                : '账号仅支持字母、数字和 . _ @ -';
              setErrors((current) => ({ ...current, username: message }));
            }}
          />
        </Field>
        <Field label="密码" htmlFor="login-password" error={errors.password} required>
          <Input
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
            maxLength={72}
            required
            value={password}
            disabled={login.isPending}
            onChange={(event) => {
              setPassword(event.target.value);
              setErrors((current) => ({ ...current, password: undefined }));
            }}
            onInvalid={() => setErrors((current) => ({ ...current, password: '请输入密码' }))}
          />
        </Field>
        <Button variant="primary" type="submit" className="auth-submit" loading={login.isPending}>
          登录
        </Button>
      </form>
      <p className="auth-help">账号问题请联系平台管理员。</p>
    </AuthLayout>
  );
}
function Workspace({ session }: { session: Session }) {
  const cache = useQueryClient();
  const [logoutError, setLogoutError] = useState<Error>();
  const navigation = useDraftNavigation();
  const navigate = useNavigate();
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
      onNavigate={(href) => void navigate(href)}
      onLogout={() =>
        navigation.request(() => {
          setLogoutError(undefined);
          logout.mutate();
        })
      }
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
        if (error instanceof ApiError && error.status === 401) {
          client.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
          return null;
        }
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
  return (
    <DraftNavigationProvider>
      <Workspace session={session.data} />
    </DraftNavigationProvider>
  );
}
const router = createBrowserRouter([{ path: '*', element: <Root /> }]);

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <DesignProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </DesignProvider>
  </React.StrictMode>,
);
