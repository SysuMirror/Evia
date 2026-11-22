import React, { lazy, Suspense, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  createBrowserRouter,
  Navigate,
  Route,
  RouterProvider,
  Routes,
  useMatch,
  useNavigate,
  useSearchParams,
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
import {
  api,
  ApiError,
  subscribeUnauthorized,
  subscribeProjectAccessDenied,
  type Environment,
  type Project,
  type Session,
} from '@evia/api-client';
import {
  AuthLayout,
  Button,
  DesignProvider,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  Select,
  WorkspaceShell,
} from '@evia/design-system';
import { DraftNavigationProvider, useDraftNavigation } from './draft-navigation';
import { NavigationIcon, type WorkspaceSection } from './navigation-icons';
const ProjectsPage = lazy(() =>
  import('./projects').then((module) => ({ default: module.ProjectsPage })),
);
const CatalogPage = lazy(() =>
  import('./catalog').then((module) => ({ default: module.CatalogPage })),
);
const ReleasesPage = lazy(() =>
  import('./releases').then((module) => ({ default: module.ReleasesPage })),
);
const SourcesPage = lazy(() =>
  import('./sources').then((module) => ({ default: module.SourcesPage })),
);
const AnalysisPage = lazy(() =>
  import('./analysis').then((module) => ({ default: module.AnalysisPage })),
);
const DashboardsPage = lazy(() =>
  import('./dashboards').then((module) => ({ default: module.DashboardsPage })),
);
const SavedAnalysesPage = lazy(() =>
  import('./saved-analyses').then((module) => ({ default: module.SavedAnalysesPage })),
);
const ReportsPage = lazy(() =>
  import('./reports/reports').then((module) => ({ default: module.ReportsPage })),
);
const MachineAccessPage = lazy(() =>
  import('./machine-access').then((module) => ({ default: module.MachineAccessPage })),
);
const GovernancePage = lazy(() =>
  import('./governance/governance').then((module) => ({ default: module.GovernancePage })),
);
const ErasurePage = lazy(() =>
  import('./erasure/erasure').then((module) => ({ default: module.ErasurePage })),
);
const IdentityPage = lazy(() =>
  import('./identity/identity').then((module) => ({ default: module.IdentityPage })),
);
const SessionReplayPage = lazy(() =>
  import('./session-replay/session-replay').then((module) => ({
    default: module.SessionReplayPage,
  })),
);
const ExportsPage = lazy(() =>
  import('./exports/exports').then((module) => ({ default: module.ExportsPage })),
);
const RetentionPage = lazy(() =>
  import('./retention/retention').then((module) => ({ default: module.RetentionPage })),
);
const ArchivingPage = lazy(() =>
  import('./archiving/archiving').then((module) => ({ default: module.ArchivingPage })),
);
const DiagnosticsPage = lazy(() =>
  import('./diagnostics/diagnostics').then((module) => ({ default: module.DiagnosticsPage })),
);
const MetricsPage = lazy(() =>
  import('./metrics/metrics').then((module) => ({ default: module.MetricsPage })),
);
const FormulaMetricsPage = lazy(() =>
  import('./formula-metrics/formula-metrics').then((module) => ({
    default: module.FormulaMetricsPage,
  })),
);
const environments = [
  { value: 'development', label: '开发环境' },
  { value: 'staging', label: '预发环境' },
  { value: 'production', label: '生产环境' },
];

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
subscribeUnauthorized(() => {
  client.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
  client.setQueryData(['session'], null);
});
subscribeProjectAccessDenied((projectID) => {
  client.removeQueries({
    predicate: (query) => query.queryKey[0] !== 'project' && query.queryKey.includes(projectID),
  });
  // Hide the current workspace immediately. A user-initiated reload rechecks access
  // without recursively retrying a project read that itself denied authorization.
  client.setQueryData(['project', projectID], null);
  void client.invalidateQueries({ queryKey: ['projects'], refetchType: 'none' });
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
  const match = useMatch('/projects/:projectID/*');
  const projectID = match?.params.projectID;
  const [parameters, setParameters] = useSearchParams();
  const rawEnvironment = parameters.get('environment') ?? 'development';
  const validEnvironment = environments.some((item) => item.value === rawEnvironment);
  const environment = (validEnvironment ? rawEnvironment : 'development') as Environment;
  const project = useQuery<Project | null>({
    queryKey: ['project', projectID],
    queryFn: ({ signal }) => api.getProject(projectID!, signal),
    enabled: !!projectID,
  });
  const sections: { path: WorkspaceSection; label: string; component: typeof CatalogPage }[] = [
    { path: 'events', label: '事件管理', component: CatalogPage },
    { path: 'releases', label: '发布管理', component: ReleasesPage },
    { path: 'sources', label: '接入来源', component: SourcesPage },
    { path: 'diagnostics', label: '采集诊断', component: DiagnosticsPage },
    { path: 'analysis', label: '数据分析', component: AnalysisPage },
    { path: 'saved-analyses', label: '保存的分析', component: SavedAnalysesPage },
    { path: 'dashboards', label: '自定义看板', component: DashboardsPage },
    { path: 'reports', label: '分析报告', component: ReportsPage },
    ...(project.data?.permissions.includes('machine_principals.manage')
      ? [{ path: 'machine-access' as const, label: '机器访问', component: MachineAccessPage }]
      : []),
    ...(project.data?.permissions.includes('settings.read')
      ? [{ path: 'governance' as const, label: '项目治理', component: GovernancePage }]
      : []),
    ...(project.data?.permissions.includes('queries.read')
      ? [{ path: 'identity' as const, label: '身份修订', component: IdentityPage }]
      : []),
    ...(project.data?.permissions.includes('content.read')
      ? [{ path: 'session-replay' as const, label: '会话回放', component: SessionReplayPage }]
      : []),
    ...(project.data?.permissions.includes('data.erasure')
      ? [{ path: 'erasure' as const, label: '数据删除', component: ErasurePage }]
      : []),
    ...(project.data?.permissions.includes('data.export')
      ? [{ path: 'exports' as const, label: '数据导出', component: ExportsPage }]
      : []),
    ...(project.data?.permissions.includes('settings.write')
      ? [
          { path: 'retention' as const, label: '数据保留', component: RetentionPage },
          { path: 'archiving' as const, label: '归档回补', component: ArchivingPage },
        ]
      : []),
    ...(project.data?.permissions.includes('metrics.read')
      ? [
          { path: 'metrics' as const, label: '独立指标', component: MetricsPage },
          {
            path: 'formula-metrics' as const,
            label: '复合公式指标',
            component: FormulaMetricsPage,
          },
        ]
      : []),
  ];
  const navigationGroup = (path: WorkspaceSection) => {
    if (['events', 'releases', 'sources', 'diagnostics'].includes(path)) return '数据接入';
    if (
      [
        'analysis',
        'saved-analyses',
        'dashboards',
        'reports',
        'session-replay',
        'identity',
      ].includes(path)
    )
      return '洞察分析';
    if (['metrics', 'formula-metrics'].includes(path)) return '指标体系';
    return '项目管理';
  };
  const navigationItems = projectID
    ? sections.map((section) => ({
        href: `/projects/${encodeURIComponent(projectID)}/${section.path}?environment=${environment}`,
        label: section.label,
        icon: <NavigationIcon section={section.path} />,
        active: match?.params['*'] === section.path,
        group: navigationGroup(section.path),
      }))
    : undefined;
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
      navigation={navigationItems}
      projectContext={
        projectID ? (
          <div className="workspace-project-context">
            <Button
              variant="link"
              className="workspace-project-back"
              onClick={() => navigation.request(() => void navigate('/projects'))}
            >
              <span aria-hidden="true">←</span>
              返回项目列表
            </Button>
            <strong className="workspace-project-name" title={project.data?.name}>
              {project.data?.name ?? '项目工作区'}
            </strong>
            <Field
              className="workspace-project-environment"
              label="当前环境"
              htmlFor="workspace-environment"
            >
              <Select
                id="workspace-environment"
                value={environment}
                options={environments}
                disabled={logout.isPending}
                onChange={(event) => {
                  const nextEnvironment = event.target.value;
                  navigation.request(() => setParameters({ environment: nextEnvironment }));
                }}
              />
            </Field>
          </div>
        ) : undefined
      }
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
          {sections.map(({ path, component: Page }) => (
            <Route
              key={path}
              path={`/projects/:projectID/${path}`}
              element={
                !validEnvironment ? (
                  <ErrorPanel message="环境参数无效，请选择当前环境。" />
                ) : project.isPending ? (
                  <LoadingPanel label="正在加载项目" />
                ) : project.isError ? (
                  <Failure error={project.error} retry={() => void project.refetch()} />
                ) : project.data ? (
                  <Page
                    key={`${projectID}:${environment}:${[...project.data.permissions].sort().join(',')}`}
                    project={project.data}
                    session={session}
                    environment={environment}
                  />
                ) : (
                  <ErrorPanel
                    message="项目权限已变化，请重新加载项目。"
                    retry={() => void project.refetch()}
                  />
                )
              }
            />
          ))}
          <Route
            path="/projects/:projectID"
            element={<Navigate to={`events?environment=${environment}`} replace />}
          />
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
