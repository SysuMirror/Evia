import type { components } from './schema.gen';
export type { components, paths } from './schema.gen';
export type Session = components['schemas']['Session'];
export type Project = components['schemas']['Project'];
export type ProjectInput = components['schemas']['ProjectInput'];
export type ProjectList = components['schemas']['ProjectList'];
export type EventInput = components['schemas']['EventInput'];
export type EventDefinition = components['schemas']['EventDefinition'];
export type EventDefinitionList = components['schemas']['EventDefinitionList'];
export type DraftInput = components['schemas']['DraftInput'];
export type Draft = components['schemas']['Draft'];
export type ValidationInput = components['schemas']['ValidationInput'];
export type ValidationResult = components['schemas']['ValidationResult'];
export type ReleaseInput = components['schemas']['ReleaseInput'];
export type Release = components['schemas']['Release'];
export type ReleaseList = components['schemas']['ReleaseList'];
export type EventVersion = components['schemas']['EventVersion'];
export type Activation = components['schemas']['Activation'];
export type ActivationInput = components['schemas']['ActivationInput'];
export type SourceInput = components['schemas']['SourceInput'];
export type Source = components['schemas']['Source'];
export type SourceUpdate = components['schemas']['SourceUpdate'];
export type SourceList = components['schemas']['SourceList'];
export type SourceIssued = components['schemas']['SourceIssued'];
export type CredentialRotation = components['schemas']['CredentialRotation'];
export type MachinePermission = components['schemas']['MachinePermission'];
export type MachinePrincipal = components['schemas']['MachinePrincipal'];
export type MachineCredential = components['schemas']['MachineCredential'];
export type MachinePrincipalDetail = components['schemas']['MachinePrincipalDetail'];
export type MachinePrincipalList = components['schemas']['MachinePrincipalList'];
export type MachinePrincipalInput = components['schemas']['MachinePrincipalInput'];
export type MachineCredentialRotation = components['schemas']['MachineCredentialRotation'];
export type MachineIssued = components['schemas']['MachineIssued'];
export type ErasureInput = components['schemas']['ErasureInput'];
export type ErasureJob = components['schemas']['ErasureJob'];
export type ErasureList = components['schemas']['ErasureList'];
export type ExportInput = components['schemas']['ExportInput'];
export type ExportJob = components['schemas']['ExportJob'];
export type ExportList = components['schemas']['ExportList'];
export type ExportGrant = components['schemas']['ExportGrant'];
export type RetentionPolicy = components['schemas']['RetentionPolicy'];
export type RetentionPolicyInput = components['schemas']['RetentionPolicyInput'];
export type RetentionSweep = components['schemas']['RetentionSweep'];
export type RetentionSweepList = components['schemas']['RetentionSweepList'];
export type ArchiveManifest = components['schemas']['ArchiveManifest'];
export type ArchiveManifestList = components['schemas']['ArchiveManifestList'];
export type BackfillInput = components['schemas']['BackfillInput'];
export type BackfillJob = components['schemas']['BackfillJob'];
export type BackfillList = components['schemas']['BackfillList'];
export type Diagnostics = components['schemas']['Diagnostics'];
export type CollectionQuota = components['schemas']['CollectionQuota'];
export type CollectionQuotaInput = components['schemas']['CollectionQuotaInput'];
export type TypeScriptArtifact = components['schemas']['TypeScriptArtifact'];
export type Report = components['schemas']['Report'];
export type ReportInput = components['schemas']['ReportInput'];
export type ReportList = components['schemas']['ReportList'];
export type ReportSection = components['schemas']['ReportSection'];
export type ReportSectionInput = components['schemas']['ReportSectionInput'];
export type ReportDefinition = components['schemas']['ReportDefinition'];
export type ReportDefinitionInput = components['schemas']['ReportDefinitionInput'];
// The generator exposes JSON Schema's annotation as a property; it is not wire data.
export type QueryRequest = Omit<components['schemas']['query-request.schema'], '$defs'>;
export type QueryJob = components['schemas']['query-job.schema'];
export type QueryResult = components['schemas']['query-result.schema'];
export type SavedAnalysisTemplate = components['schemas']['SavedAnalysisTemplate'];
export type SavedAnalysisDefinition = components['schemas']['SavedAnalysisDefinition'];
export type SavedAnalysisDefinitionInput = components['schemas']['SavedAnalysisDefinitionInput'];
export type MetricReference = components['schemas']['MetricReference'];
export type SavedAnalysis = components['schemas']['SavedAnalysis'];
export type SavedAnalysisDraft = components['schemas']['SavedAnalysisDraft'];
export type SavedAnalysisVersion = components['schemas']['SavedAnalysisVersion'];
export type SavedAnalysisValidation = components['schemas']['SavedAnalysisValidation'];
export type SavedAnalysisCreate = components['schemas']['SavedAnalysisCreate'];
export type SavedAnalysisUpdate = components['schemas']['SavedAnalysisUpdate'];
export type SavedAnalysisValidate = components['schemas']['SavedAnalysisValidate'];
export type SavedAnalysisPublish = components['schemas']['SavedAnalysisPublish'];
export type SavedAnalysisExecute = components['schemas']['SavedAnalysisExecute'];
export type SavedAnalysisList = components['schemas']['SavedAnalysisList'];
export type SavedAnalysisVersionList = components['schemas']['SavedAnalysisVersionList'];
export type SavedAnalysisChange = components['schemas']['SavedAnalysisChange'];
export type Dashboard = components['schemas']['Dashboard'];
export type DashboardDefinition = components['schemas']['DashboardDefinition'];
export type DashboardWidget = components['schemas']['DashboardWidget'];
export type DashboardLayout = components['schemas']['DashboardLayout'];
export type DashboardDraft = components['schemas']['DashboardDraft'];
export type DashboardDependency = components['schemas']['DashboardDependency'];
export type DashboardDependencies = components['schemas']['DashboardDependencies'];
export type DashboardVersion = components['schemas']['DashboardVersion'];
export type DashboardChange = components['schemas']['DashboardChange'];
export type DashboardValidation = components['schemas']['DashboardValidation'];
export type DashboardCreate = components['schemas']['DashboardCreate'];
export type DashboardUpdate = components['schemas']['DashboardUpdate'];
export type DashboardValidate = components['schemas']['DashboardValidate'];
export type DashboardPublish = components['schemas']['DashboardPublish'];
export type DashboardWidgetFilter = components['schemas']['DashboardWidgetFilter'];
export type DashboardExecute = components['schemas']['DashboardExecute'];
export type DashboardWidgetRun = components['schemas']['DashboardWidgetRun'];
export type DashboardRun = components['schemas']['DashboardRun'];
export type DashboardVersionList = components['schemas']['DashboardVersionList'];
export type DashboardList = components['schemas']['DashboardList'];
export type ProjectSettings = components['schemas']['ProjectSettings'];
export type ProjectSettingsUpdate = components['schemas']['ProjectSettingsUpdate'];
export type ProjectMember = components['schemas']['ProjectMember'];
export type ProjectMemberAdd = components['schemas']['ProjectMemberAdd'];
export type ProjectMemberUpdate = components['schemas']['ProjectMemberUpdate'];
export type ProjectMemberResult = components['schemas']['ProjectMemberResult'];
export type ProjectMemberList = components['schemas']['ProjectMemberList'];
export type ProjectAuditEntry = components['schemas']['ProjectAuditEntry'];
export type ProjectAuditList = components['schemas']['ProjectAuditList'];
export type MetricDefinition = components['schemas']['MetricDefinition'];
export type MetricDraft = components['schemas']['MetricDraft'];
export type MetricVersion = components['schemas']['MetricVersion'];
export type MetricValidation = components['schemas']['MetricValidation'];
export type MetricChange = components['schemas']['MetricChange'];
export type MetricRecord = components['schemas']['MetricRecord'];
export type MetricList = components['schemas']['MetricList'];
export type MetricVersionList = components['schemas']['MetricVersionList'];
export type MetricCreate = components['schemas']['MetricCreate'];
export type MetricUpdate = components['schemas']['MetricUpdate'];
export type MetricValidate = components['schemas']['MetricValidate'];
export type MetricPublish = components['schemas']['MetricPublish'];
export type MetricExecute = components['schemas']['MetricExecute'];
export type MetricPage = { environment: Environment; cursor?: string; limit?: number };
export type MetricVersionPage = { cursor?: string; limit?: number };
export type FormulaExpression = components['schemas']['FormulaExpression'];
export type FormulaDefinition = components['schemas']['FormulaDefinition'];
export type FormulaMetricDefinition = components['schemas']['FormulaMetricDefinition'];
export type FormulaMetricDependency = components['schemas']['FormulaMetricDependency'];
export type FormulaDependencyManifest = components['schemas']['FormulaDependencyManifest'];
export type FormulaMetricChange = components['schemas']['FormulaMetricChange'];
export type FormulaMetricRecord = components['schemas']['FormulaMetricRecord'];
export type FormulaMetricDraft = components['schemas']['FormulaMetricDraft'];
export type FormulaMetricVersion = components['schemas']['FormulaMetricVersion'];
export type FormulaMetricValidation = components['schemas']['FormulaMetricValidation'];
export type FormulaMetricCreate = components['schemas']['FormulaMetricCreate'];
export type FormulaMetricUpdate = components['schemas']['FormulaMetricUpdate'];
export type FormulaMetricValidate = components['schemas']['FormulaMetricValidate'];
export type FormulaMetricPublish = components['schemas']['FormulaMetricPublish'];
export type FormulaMetricList = components['schemas']['FormulaMetricList'];
export type FormulaMetricVersionList = components['schemas']['FormulaMetricVersionList'];
export type FormulaMetricPage = { environment: Environment; cursor?: string; limit?: number };
export type FormulaMetricVersionPage = { cursor?: string; limit?: number };
export type ProjectMemberPage = { cursor?: string; limit?: number };
export type ProjectAuditPage = {
  from: string;
  to: string;
  action?: string;
  actorID?: string;
  cursor?: string;
  limit?: number;
};
export type Environment = Release['environment'];
export type CatalogPage = {
  environment?: Environment;
  cursor?: string;
  limit?: number;
  view?: 'active' | 'archived';
};
export type SavedAnalysisPage = {
  environment: Environment;
  cursor?: string;
  limit?: number;
  view?: 'active' | 'archived';
};
export type DashboardPage = SavedAnalysisPage & { view?: 'active' | 'archived' };
export type SavedAnalysisVersionPage = { cursor?: string; limit?: number };
export type MachinePrincipalPage = { cursor?: string; limit?: number };
export type QueryResultPage = { cursor?: string; limit?: number };

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestID?: string,
    public details?: components['schemas']['Problem']['details'],
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

const unauthorizedListeners = new Set<() => void>();
const projectAccessDeniedListeners = new Set<(projectID: string) => void>();

export function subscribeUnauthorized(listener: () => void): () => void {
  unauthorizedListeners.add(listener);
  return () => {
    unauthorizedListeners.delete(listener);
  };
}

export function subscribeProjectAccessDenied(listener: (projectID: string) => void): () => void {
  projectAccessDeniedListeners.add(listener);
  return () => {
    projectAccessDeniedListeners.delete(listener);
  };
}

async function request<T>(
  path: string,
  options: {
    method?: string;
    body?: unknown;
    csrf?: string;
    headers?: { 'If-Match'?: string; 'Idempotency-Key'?: string };
    signal?: AbortSignal;
  } = {},
): Promise<T> {
  const headers = new Headers({ Accept: 'application/json' });
  if (options.body !== undefined) headers.set('Content-Type', 'application/json');
  if (options.csrf) headers.set('X-CSRF-Token', options.csrf);
  for (const [name, value] of Object.entries(options.headers ?? {})) {
    if (value !== undefined) headers.set(name, value);
  }
  const timeout = AbortSignal.timeout(15000);
  const signal = options.signal ? AbortSignal.any([timeout, options.signal]) : timeout;
  let response: Response;
  try {
    response = await fetch(path, {
      method: options.method ?? 'GET',
      credentials: 'same-origin',
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal,
    });
  } catch (error) {
    if (options.signal?.aborted) throw error;
    throw new ApiError(0, 'NETWORK_ERROR', '无法连接服务，请检查网络后重试。');
  }
  if (!response.ok) {
    if (response.status === 401 && path !== '/api/v1/session/login') {
      for (const listener of unauthorizedListeners) listener();
    }
    let problem: Partial<components['schemas']['Problem']> = {};
    try {
      problem = await response.json();
    } catch {
      /* Reverse proxies may return non-JSON failures. */
    }
    if (problem.code === 'PROJECT_NOT_FOUND' || problem.code === 'PROJECT_FORBIDDEN') {
      const project = /^\/api\/v1\/projects\/([^/?#]+)(?:\/|[?#]|$)/.exec(path);
      if (project?.[1]) {
        const projectID = decodeURIComponent(project[1]);
        for (const listener of projectAccessDeniedListeners) listener(projectID);
      }
    }
    throw new ApiError(
      response.status,
      typeof problem.code === 'string' ? problem.code : 'HTTP_ERROR',
      typeof problem.message === 'string' ? problem.message : `请求失败（${response.status}）`,
      response.headers.get('X-Request-ID') ?? undefined,
      problem.details,
    );
  }
  if (response.status === 204) return undefined as T;
  try {
    return (await response.json()) as T;
  } catch (error) {
    if (options.signal?.aborted) throw error;
    // A successful status can precede a failed body read after a committed write.
    throw new ApiError(
      0,
      'INVALID_RESPONSE',
      '服务响应未完整接收，请重新加载确认操作结果。',
      response.headers.get('X-Request-ID') ?? undefined,
    );
  }
}
const projectPath = (projectID: string) => `/api/v1/projects/${encodeURIComponent(projectID)}`;
const eventPath = (projectID: string, eventKey: string) =>
  `${projectPath(projectID)}/event-definitions/${encodeURIComponent(eventKey)}`;
const dashboardPath = (projectID: string, dashboardID: string) =>
  `${projectPath(projectID)}/dashboards/${encodeURIComponent(dashboardID)}`;
const savedAnalysisPath = (projectID: string, analysisID: string) =>
  `${projectPath(projectID)}/saved-analyses/${encodeURIComponent(analysisID)}`;
const machinePrincipalPath = (projectID: string, principalID: string) =>
  `${projectPath(projectID)}/machine-principals/${encodeURIComponent(principalID)}`;
const erasureJobPath = (projectID: string, jobID: string) =>
  `${projectPath(projectID)}/erasure-jobs/${encodeURIComponent(jobID)}`;
const exportJobPath = (projectID: string, jobID: string) =>
  `${projectPath(projectID)}/exports/${encodeURIComponent(jobID)}`;
const metricPath = (projectID: string, metricID: string) =>
  `${projectPath(projectID)}/metrics/${encodeURIComponent(metricID)}`;
const formulaMetricPath = (projectID: string, metricID: string) =>
  `${projectPath(projectID)}/formula-metrics/${encodeURIComponent(metricID)}`;
function catalogQuery(page: CatalogPage): string {
  const query = new URLSearchParams();
  if (page.environment !== undefined) query.set('environment', page.environment);
  if (page.cursor !== undefined) query.set('cursor', page.cursor);
  if (page.limit !== undefined) query.set('limit', String(page.limit));
  if (page.view !== undefined) query.set('view', page.view);
  return query.size ? `?${query.toString()}` : '';
}
function queryResultQuery(page: QueryResultPage): string {
  const query = new URLSearchParams();
  if (page.cursor !== undefined) query.set('cursor', page.cursor);
  if (page.limit !== undefined) query.set('limit', String(page.limit));
  return query.size ? `?${query.toString()}` : '';
}
function auditQuery(page: ProjectAuditPage): string {
  const query = new URLSearchParams();
  query.set('from', page.from);
  query.set('to', page.to);
  if (page.action) query.set('action', page.action);
  if (page.actorID) query.set('actor_id', page.actorID);
  if (page.cursor !== undefined) query.set('cursor', page.cursor);
  if (page.limit !== undefined) query.set('limit', String(page.limit));
  return `?${query.toString()}`;
}
export const api = {
  session: (signal?: AbortSignal) => request<Session>('/api/v1/session', { signal }),
  login: (body: components['schemas']['LoginInput']) =>
    request<Session>('/api/v1/session/login', { method: 'POST', body }),
  logout: (csrf: string) => request<void>('/api/v1/session', { method: 'DELETE', csrf }),
  projects: (cursor?: string, signal?: AbortSignal) =>
    request<ProjectList>(
      `/api/v1/projects?limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
      { signal },
    ),
  createProject: (body: ProjectInput, csrf: string) =>
    request<Project>('/api/v1/projects', { method: 'POST', body, csrf }),
  listEventDefinitions: (projectID: string, page: CatalogPage = {}, signal?: AbortSignal) =>
    request<EventDefinitionList>(
      `${projectPath(projectID)}/event-definitions${catalogQuery(page)}`,
      { signal },
    ),
  createEventDefinition: (projectID: string, body: EventInput, csrf: string) =>
    request<EventDefinition>(`${projectPath(projectID)}/event-definitions`, {
      method: 'POST',
      body,
      csrf,
    }),
  getEventVersion: (
    projectID: string,
    eventKey: string,
    schemaVersion: number,
    signal?: AbortSignal,
  ) =>
    request<EventVersion>(`${eventPath(projectID, eventKey)}/versions/${schemaVersion}`, {
      signal,
    }),
  getEventDraft: (projectID: string, eventKey: string, signal?: AbortSignal) =>
    request<Draft>(`${eventPath(projectID, eventKey)}/draft`, { signal }),
  updateEventDraft: (
    projectID: string,
    eventKey: string,
    revision: number,
    body: DraftInput,
    csrf: string,
  ) =>
    request<Draft>(`${eventPath(projectID, eventKey)}/draft`, {
      method: 'PATCH',
      body,
      csrf,
      headers: { 'If-Match': `"${revision}"` },
    }),
  validateEventDraft: (projectID: string, eventKey: string, body: ValidationInput, csrf: string) =>
    request<ValidationResult>(`${eventPath(projectID, eventKey)}/validate`, {
      method: 'POST',
      body,
      csrf,
    }),
  listReleases: (projectID: string, page: CatalogPage = {}, signal?: AbortSignal) =>
    request<ReleaseList>(`${projectPath(projectID)}/releases${catalogQuery(page)}`, { signal }),
  createRelease: (projectID: string, body: ReleaseInput, idempotencyKey: string, csrf: string) =>
    request<Release>(`${projectPath(projectID)}/releases`, {
      method: 'POST',
      body,
      csrf,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getRelease: (projectID: string, releaseID: string, signal?: AbortSignal) =>
    request<Release>(`${projectPath(projectID)}/releases/${encodeURIComponent(releaseID)}`, {
      signal,
    }),
  getProject: (projectID: string, signal?: AbortSignal) =>
    request<Project>(projectPath(projectID), { signal }),
  listMembers: (projectID: string, page: ProjectMemberPage = {}, signal?: AbortSignal) =>
    request<ProjectMemberList>(
      `${projectPath(projectID)}/members${catalogQuery({ cursor: page.cursor, limit: page.limit })}`,
      { signal },
    ),
  addMember: (
    projectID: string,
    revision: number,
    body: ProjectMemberAdd,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ProjectMemberResult>(`${projectPath(projectID)}/members`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"`, 'Idempotency-Key': idempotencyKey },
    }),
  updateMember: (
    projectID: string,
    userID: string,
    revision: number,
    body: ProjectMemberUpdate,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ProjectMemberResult>(
      `${projectPath(projectID)}/members/${encodeURIComponent(userID)}`,
      {
        method: 'PATCH',
        body,
        csrf,
        signal,
        headers: { 'If-Match': `"${revision}"`, 'Idempotency-Key': idempotencyKey },
      },
    ),
  removeMember: (
    projectID: string,
    userID: string,
    revision: number,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ProjectMemberResult>(
      `${projectPath(projectID)}/members/${encodeURIComponent(userID)}`,
      {
        method: 'DELETE',
        csrf,
        signal,
        headers: { 'If-Match': `"${revision}"`, 'Idempotency-Key': idempotencyKey },
      },
    ),
  getSettings: (projectID: string, signal?: AbortSignal) =>
    request<ProjectSettings>(`${projectPath(projectID)}/settings`, { signal }),
  updateSettings: (
    projectID: string,
    revision: number,
    body: ProjectSettingsUpdate,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ProjectSettings>(`${projectPath(projectID)}/settings`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"`, 'Idempotency-Key': idempotencyKey },
    }),
  listAuditLogs: (projectID: string, page: ProjectAuditPage, signal?: AbortSignal) =>
    request<ProjectAuditList>(`${projectPath(projectID)}/audit-logs${auditQuery(page)}`, {
      signal,
    }),
  listMetrics: (projectID: string, page: MetricPage, signal?: AbortSignal) =>
    request<MetricList>(`${projectPath(projectID)}/metrics${catalogQuery(page)}`, { signal }),
  createMetric: (
    projectID: string,
    body: MetricCreate,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MetricDraft>(`${projectPath(projectID)}/metrics`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getMetric: (projectID: string, metricID: string, signal?: AbortSignal) =>
    request<MetricDraft>(metricPath(projectID, metricID), { signal }),
  getMetricDraft: (projectID: string, metricID: string, signal?: AbortSignal) =>
    request<MetricDraft>(`${metricPath(projectID, metricID)}/draft`, { signal }),
  updateMetricDraft: (
    projectID: string,
    metricID: string,
    revision: number,
    body: MetricUpdate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MetricDraft>(`${metricPath(projectID, metricID)}/draft`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"` },
    }),
  validateMetric: (
    projectID: string,
    metricID: string,
    body: MetricValidate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MetricValidation>(`${metricPath(projectID, metricID)}/validations`, {
      method: 'POST',
      body,
      csrf,
      signal,
    }),
  listMetricVersions: (
    projectID: string,
    metricID: string,
    page: MetricVersionPage = {},
    signal?: AbortSignal,
  ) =>
    request<MetricVersionList>(
      `${metricPath(projectID, metricID)}/versions${catalogQuery({
        cursor: page.cursor,
        limit: page.limit,
      })}`,
      { signal },
    ),
  getMetricVersion: (projectID: string, metricID: string, version: number, signal?: AbortSignal) =>
    request<MetricVersion>(
      `${metricPath(projectID, metricID)}/versions/${encodeURIComponent(String(version))}`,
      { signal },
    ),
  publishMetric: (
    projectID: string,
    metricID: string,
    body: MetricPublish,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MetricVersion>(`${metricPath(projectID, metricID)}/versions`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  executeMetricVersion: (
    projectID: string,
    metricID: string,
    version: number,
    body: MetricExecute,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<QueryJob>(
      `${metricPath(projectID, metricID)}/versions/${encodeURIComponent(String(version))}/queries`,
      {
        method: 'POST',
        body,
        csrf,
        signal,
        headers: { 'Idempotency-Key': idempotencyKey },
      },
    ),
  listFormulaMetrics: (projectID: string, page: FormulaMetricPage, signal?: AbortSignal) =>
    request<FormulaMetricList>(`${projectPath(projectID)}/formula-metrics${catalogQuery(page)}`, {
      signal,
    }),
  createFormulaMetric: (
    projectID: string,
    body: FormulaMetricCreate,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<FormulaMetricDraft>(`${projectPath(projectID)}/formula-metrics`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getFormulaMetric: (projectID: string, metricID: string, signal?: AbortSignal) =>
    request<FormulaMetricRecord>(formulaMetricPath(projectID, metricID), { signal }),
  getFormulaMetricDraft: (projectID: string, metricID: string, signal?: AbortSignal) =>
    request<FormulaMetricDraft>(`${formulaMetricPath(projectID, metricID)}/draft`, { signal }),
  updateFormulaMetricDraft: (
    projectID: string,
    metricID: string,
    revision: number,
    body: FormulaMetricUpdate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<FormulaMetricDraft>(`${formulaMetricPath(projectID, metricID)}/draft`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"` },
    }),
  validateFormulaMetric: (
    projectID: string,
    metricID: string,
    body: FormulaMetricValidate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<FormulaMetricValidation>(`${formulaMetricPath(projectID, metricID)}/validations`, {
      method: 'POST',
      body,
      csrf,
      signal,
    }),
  listFormulaMetricVersions: (
    projectID: string,
    metricID: string,
    page: FormulaMetricVersionPage = {},
    signal?: AbortSignal,
  ) =>
    request<FormulaMetricVersionList>(
      `${formulaMetricPath(projectID, metricID)}/versions${catalogQuery({
        cursor: page.cursor,
        limit: page.limit,
      })}`,
      { signal },
    ),
  getFormulaMetricVersion: (
    projectID: string,
    metricID: string,
    version: number,
    signal?: AbortSignal,
  ) =>
    request<FormulaMetricVersion>(
      `${formulaMetricPath(projectID, metricID)}/versions/${encodeURIComponent(String(version))}`,
      { signal },
    ),
  publishFormulaMetric: (
    projectID: string,
    metricID: string,
    body: FormulaMetricPublish,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<FormulaMetricVersion>(`${formulaMetricPath(projectID, metricID)}/versions`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  executeFormulaMetricVersion: (
    projectID: string,
    metricID: string,
    version: number,
    body: MetricExecute,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<QueryJob>(
      `${formulaMetricPath(projectID, metricID)}/versions/${encodeURIComponent(
        String(version),
      )}/queries`,
      {
        method: 'POST',
        body,
        csrf,
        signal,
        headers: { 'Idempotency-Key': idempotencyKey },
      },
    ),
  getActivation: (projectID: string, environment: Environment, signal?: AbortSignal) =>
    request<Activation>(
      `${projectPath(projectID)}/release-activations${catalogQuery({ environment })}`,
      { signal },
    ),
  activateRelease: (
    projectID: string,
    body: ActivationInput,
    idempotencyKey: string,
    csrf: string,
  ) =>
    request<Activation>(`${projectPath(projectID)}/release-activations`, {
      method: 'POST',
      body,
      csrf,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  listSources: (projectID: string, page: CatalogPage = {}, signal?: AbortSignal) =>
    request<SourceList>(`${projectPath(projectID)}/sources${catalogQuery(page)}`, { signal }),
  getSource: (projectID: string, sourceID: string, signal?: AbortSignal) =>
    request<Source>(`${projectPath(projectID)}/sources/${encodeURIComponent(sourceID)}`, {
      signal,
    }),
  createSource: (projectID: string, body: SourceInput, csrf: string) =>
    request<SourceIssued>(`${projectPath(projectID)}/sources`, { method: 'POST', body, csrf }),
  updateSource: (
    projectID: string,
    sourceID: string,
    revision: number,
    body: SourceUpdate,
    csrf: string,
  ) =>
    request<Source>(`${projectPath(projectID)}/sources/${encodeURIComponent(sourceID)}`, {
      method: 'PATCH',
      body,
      csrf,
      headers: { 'If-Match': `"${revision}"` },
    }),
  rotateCredential: (
    projectID: string,
    sourceID: string,
    body: CredentialRotation,
    idempotencyKey: string,
    csrf: string,
  ) =>
    request<SourceIssued>(
      `${projectPath(projectID)}/sources/${encodeURIComponent(sourceID)}/credential-rotations`,
      {
        method: 'POST',
        body,
        csrf,
        headers: { 'Idempotency-Key': idempotencyKey },
      },
    ),
  listMachinePrincipals: (
    projectID: string,
    page: MachinePrincipalPage = {},
    signal?: AbortSignal,
  ) =>
    request<MachinePrincipalList>(
      `${projectPath(projectID)}/machine-principals${catalogQuery({ cursor: page.cursor, limit: page.limit })}`,
      { signal },
    ),
  getMachinePrincipal: (projectID: string, principalID: string, signal?: AbortSignal) =>
    request<MachinePrincipalDetail>(machinePrincipalPath(projectID, principalID), { signal }),
  createMachinePrincipal: (
    projectID: string,
    body: MachinePrincipalInput,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MachineIssued>(`${projectPath(projectID)}/machine-principals`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  rotateMachineCredential: (
    projectID: string,
    principalID: string,
    revision: number,
    body: MachineCredentialRotation,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MachineIssued>(`${machinePrincipalPath(projectID, principalID)}/credential-rotations`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"`, 'Idempotency-Key': idempotencyKey },
    }),
  revokeMachinePrincipal: (
    projectID: string,
    principalID: string,
    revision: number,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MachinePrincipal>(machinePrincipalPath(projectID, principalID), {
      method: 'DELETE',
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"` },
    }),
  revokeMachineCredential: (
    projectID: string,
    principalID: string,
    credentialID: string,
    revision: number,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<MachinePrincipal>(
      `${machinePrincipalPath(projectID, principalID)}/credentials/${encodeURIComponent(credentialID)}`,
      {
        method: 'DELETE',
        csrf,
        signal,
        headers: { 'If-Match': `"${revision}"` },
      },
    ),
  listSavedAnalyses: (projectID: string, page: SavedAnalysisPage, signal?: AbortSignal) =>
    request<SavedAnalysisList>(`${projectPath(projectID)}/saved-analyses${catalogQuery(page)}`, {
      signal,
    }),
  createSavedAnalysis: (
    projectID: string,
    body: SavedAnalysisCreate,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysisDraft>(`${projectPath(projectID)}/saved-analyses`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getSavedAnalysis: (projectID: string, analysisID: string, signal?: AbortSignal) =>
    request<SavedAnalysis>(savedAnalysisPath(projectID, analysisID), { signal }),
  archiveSavedAnalysis: (
    projectID: string,
    analysisID: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysis>(`${savedAnalysisPath(projectID, analysisID)}/archive`, {
      method: 'POST',
      csrf,
      signal,
    }),
  unarchiveSavedAnalysis: (
    projectID: string,
    analysisID: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysis>(`${savedAnalysisPath(projectID, analysisID)}/unarchive`, {
      method: 'POST',
      csrf,
      signal,
    }),
  getSavedAnalysisDraft: (projectID: string, analysisID: string, signal?: AbortSignal) =>
    request<SavedAnalysisDraft>(`${savedAnalysisPath(projectID, analysisID)}/draft`, { signal }),
  updateSavedAnalysisDraft: (
    projectID: string,
    analysisID: string,
    revision: number,
    body: SavedAnalysisUpdate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysisDraft>(`${savedAnalysisPath(projectID, analysisID)}/draft`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"` },
    }),
  validateSavedAnalysis: (
    projectID: string,
    analysisID: string,
    body: SavedAnalysisValidate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysisValidation>(`${savedAnalysisPath(projectID, analysisID)}/validations`, {
      method: 'POST',
      body,
      csrf,
      signal,
    }),
  listSavedAnalysisVersions: (
    projectID: string,
    analysisID: string,
    page: SavedAnalysisVersionPage = {},
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysisVersionList>(
      `${savedAnalysisPath(projectID, analysisID)}/versions${catalogQuery(page)}`,
      { signal },
    ),
  publishSavedAnalysis: (
    projectID: string,
    analysisID: string,
    body: SavedAnalysisPublish,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysisVersion>(`${savedAnalysisPath(projectID, analysisID)}/versions`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getSavedAnalysisVersion: (
    projectID: string,
    analysisID: string,
    version: number,
    signal?: AbortSignal,
  ) =>
    request<SavedAnalysisVersion>(
      `${savedAnalysisPath(projectID, analysisID)}/versions/${encodeURIComponent(String(version))}`,
      { signal },
    ),
  executeSavedAnalysisVersion: (
    projectID: string,
    analysisID: string,
    version: number,
    body: SavedAnalysisExecute,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<QueryJob>(
      `${savedAnalysisPath(projectID, analysisID)}/versions/${encodeURIComponent(String(version))}/queries`,
      {
        method: 'POST',
        body,
        csrf,
        signal,
        headers: { 'Idempotency-Key': idempotencyKey },
      },
    ),
  listDashboards: (projectID: string, page: DashboardPage, signal?: AbortSignal) =>
    request<DashboardList>(`${projectPath(projectID)}/dashboards${catalogQuery(page)}`, {
      signal,
    }),
  createDashboard: (
    projectID: string,
    body: DashboardCreate,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<DashboardDraft>(`${projectPath(projectID)}/dashboards`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getDashboard: (projectID: string, dashboardID: string, signal?: AbortSignal) =>
    request<Dashboard>(dashboardPath(projectID, dashboardID), { signal }),
  archiveDashboard: (projectID: string, dashboardID: string, csrf: string, signal?: AbortSignal) =>
    request<Dashboard>(`${dashboardPath(projectID, dashboardID)}/archive`, {
      method: 'POST',
      csrf,
      signal,
    }),
  unarchiveDashboard: (
    projectID: string,
    dashboardID: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<Dashboard>(`${dashboardPath(projectID, dashboardID)}/unarchive`, {
      method: 'POST',
      csrf,
      signal,
    }),
  getDashboardDraft: (projectID: string, dashboardID: string, signal?: AbortSignal) =>
    request<DashboardDraft>(`${dashboardPath(projectID, dashboardID)}/draft`, { signal }),
  updateDashboardDraft: (
    projectID: string,
    dashboardID: string,
    revision: number,
    body: DashboardUpdate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<DashboardDraft>(`${dashboardPath(projectID, dashboardID)}/draft`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: { 'If-Match': `"${revision}"` },
    }),
  validateDashboard: (
    projectID: string,
    dashboardID: string,
    body: DashboardValidate,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<DashboardValidation>(`${dashboardPath(projectID, dashboardID)}/validations`, {
      method: 'POST',
      body,
      csrf,
      signal,
    }),
  listDashboardVersions: (
    projectID: string,
    dashboardID: string,
    page: SavedAnalysisVersionPage = {},
    signal?: AbortSignal,
  ) =>
    request<DashboardVersionList>(
      `${dashboardPath(projectID, dashboardID)}/versions${catalogQuery(page)}`,
      { signal },
    ),
  publishDashboard: (
    projectID: string,
    dashboardID: string,
    body: DashboardPublish,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<DashboardVersion>(`${dashboardPath(projectID, dashboardID)}/versions`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  getDashboardVersion: (
    projectID: string,
    dashboardID: string,
    version: number,
    signal?: AbortSignal,
  ) =>
    request<DashboardVersion>(
      `${dashboardPath(projectID, dashboardID)}/versions/${encodeURIComponent(String(version))}`,
      { signal },
    ),
  executeDashboardVersion: (
    projectID: string,
    dashboardID: string,
    version: number,
    body: DashboardExecute,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<DashboardRun>(
      `${dashboardPath(projectID, dashboardID)}/versions/${encodeURIComponent(String(version))}/runs`,
      {
        method: 'POST',
        body,
        csrf,
        signal,
        headers: { 'Idempotency-Key': idempotencyKey },
      },
    ),
  getDashboardRun: (projectID: string, runID: string, signal?: AbortSignal) =>
    request<DashboardRun>(`${projectPath(projectID)}/dashboard-runs/${encodeURIComponent(runID)}`, {
      signal,
    }),
  cancelDashboardRun: (projectID: string, runID: string, csrf: string, signal?: AbortSignal) =>
    request<DashboardRun>(`${projectPath(projectID)}/dashboard-runs/${encodeURIComponent(runID)}`, {
      method: 'DELETE',
      csrf,
      signal,
    }),
  createQuery: (projectID: string, body: QueryRequest, csrf: string, signal?: AbortSignal) =>
    request<QueryJob>(`${projectPath(projectID)}/queries`, { method: 'POST', body, csrf, signal }),
  getQuery: (projectID: string, queryID: string, signal?: AbortSignal) =>
    request<QueryJob>(`${projectPath(projectID)}/queries/${encodeURIComponent(queryID)}`, {
      signal,
    }),
  cancelQuery: (projectID: string, queryID: string, csrf: string, signal?: AbortSignal) =>
    request<QueryJob>(`${projectPath(projectID)}/queries/${encodeURIComponent(queryID)}`, {
      method: 'DELETE',
      csrf,
      signal,
    }),
  getQueryResult: (
    projectID: string,
    queryID: string,
    options: { cursor?: string; limit?: number; signal?: AbortSignal } = {},
  ) =>
    request<QueryResult>(
      `${projectPath(projectID)}/queries/${encodeURIComponent(queryID)}/result${queryResultQuery(options)}`,
      { signal: options.signal },
    ),
  createErasureJob: (
    projectID: string,
    body: ErasureInput,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ErasureJob>(`${projectPath(projectID)}/erasure-jobs`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  listErasureJobs: (
    projectID: string,
    page: { cursor?: string; limit?: number } = {},
    signal?: AbortSignal,
  ) =>
    request<ErasureList>(
      `${projectPath(projectID)}/erasure-jobs${catalogQuery({ cursor: page.cursor, limit: page.limit })}`,
      { signal },
    ),
  getErasureJob: (projectID: string, jobID: string, signal?: AbortSignal) =>
    request<ErasureJob>(erasureJobPath(projectID, jobID), { signal }),
  getRetentionPolicy: (
    projectID: string,
    environment: RetentionPolicyInput['environment'],
    signal?: AbortSignal,
  ) =>
    request<RetentionPolicy>(
      `${projectPath(projectID)}/retention-policy?environment=${encodeURIComponent(environment)}`,
      { signal },
    ),
  putRetentionPolicy: (
    projectID: string,
    body: RetentionPolicyInput,
    expectedRevision: number,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<RetentionPolicy>(`${projectPath(projectID)}/retention-policy`, {
      method: 'PUT',
      body,
      csrf,
      signal,
      headers: {
        'If-Match': `"${expectedRevision}"`,
        'Idempotency-Key': idempotencyKey,
      },
    }),
  listRetentionSweeps: (
    projectID: string,
    options: { environment?: RetentionPolicyInput['environment']; limit?: number } = {},
    signal?: AbortSignal,
  ) => {
    const params = new URLSearchParams();
    if (options.environment) params.set('environment', options.environment);
    if (options.limit !== undefined) params.set('limit', String(options.limit));
    const query = params.toString();
    return request<RetentionSweepList>(
      `${projectPath(projectID)}/retention-sweeps${query ? `?${query}` : ''}`,
      { signal },
    );
  },
  listArchiveManifests: (
    projectID: string,
    options: {
      environment?: ArchiveManifest['environment'];
      limit?: number;
      cursor?: string;
    } = {},
    signal?: AbortSignal,
  ) => {
    const params = new URLSearchParams();
    if (options.environment) params.set('environment', options.environment);
    if (options.limit !== undefined) params.set('limit', String(options.limit));
    if (options.cursor) params.set('cursor', options.cursor);
    const query = params.toString();
    return request<ArchiveManifestList>(
      `${projectPath(projectID)}/archive-manifests${query ? `?${query}` : ''}`,
      { signal },
    );
  },
  getArchiveManifest: (projectID: string, manifestID: string, signal?: AbortSignal) =>
    request<ArchiveManifest>(
      `${projectPath(projectID)}/archive-manifests/${encodeURIComponent(manifestID)}`,
      { signal },
    ),
  createBackfillJob: (
    projectID: string,
    body: BackfillInput,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<BackfillJob>(`${projectPath(projectID)}/backfills`, {
      method: 'POST',
      body,
      csrf,
      headers: { 'Idempotency-Key': idempotencyKey },
      signal,
    }),
  listBackfillJobs: (
    projectID: string,
    options: { limit?: number; cursor?: string } = {},
    signal?: AbortSignal,
  ) => {
    const params = new URLSearchParams();
    if (options.limit !== undefined) params.set('limit', String(options.limit));
    if (options.cursor) params.set('cursor', options.cursor);
    const query = params.toString();
    return request<BackfillList>(`${projectPath(projectID)}/backfills${query ? `?${query}` : ''}`, {
      signal,
    });
  },
  getBackfillJob: (projectID: string, jobID: string, signal?: AbortSignal) =>
    request<BackfillJob>(`${projectPath(projectID)}/backfills/${encodeURIComponent(jobID)}`, {
      signal,
    }),
  cancelBackfillJob: (projectID: string, jobID: string, csrf: string, signal?: AbortSignal) =>
    request<BackfillJob>(`${projectPath(projectID)}/backfills/${encodeURIComponent(jobID)}`, {
      method: 'DELETE',
      csrf,
      signal,
    }),
  getDiagnostics: (projectID: string, signal?: AbortSignal) =>
    request<Diagnostics>(`${projectPath(projectID)}/diagnostics`, { signal }),
  getReleaseTypeScriptArtifact: (projectID: string, releaseID: string, signal?: AbortSignal) =>
    request<TypeScriptArtifact>(
      `${projectPath(projectID)}/releases/${encodeURIComponent(releaseID)}/artifacts/typescript`,
      { signal },
    ),
  getCollectionQuota: (
    projectID: string,
    environment: CollectionQuotaInput['environment'],
    signal?: AbortSignal,
  ) =>
    request<CollectionQuota>(
      `${projectPath(projectID)}/quotas?environment=${encodeURIComponent(environment)}`,
      { signal },
    ),
  patchCollectionQuota: (
    projectID: string,
    body: CollectionQuotaInput,
    expectedRevision: number,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<CollectionQuota>(`${projectPath(projectID)}/quotas`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: {
        'If-Match': `"${expectedRevision}"`,
        'Idempotency-Key': idempotencyKey,
      },
    }),
  createExportJob: (
    projectID: string,
    body: ExportInput,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ExportJob>(`${projectPath(projectID)}/exports`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  listExportJobs: (
    projectID: string,
    page: { cursor?: string; limit?: number } = {},
    signal?: AbortSignal,
  ) =>
    request<ExportList>(
      `${projectPath(projectID)}/exports${catalogQuery({ cursor: page.cursor, limit: page.limit })}`,
      { signal },
    ),
  getExportJob: (projectID: string, jobID: string, signal?: AbortSignal) =>
    request<ExportJob>(exportJobPath(projectID, jobID), { signal }),
  cancelExportJob: (projectID: string, jobID: string, csrf: string, signal?: AbortSignal) =>
    request<ExportJob>(exportJobPath(projectID, jobID), { method: 'DELETE', csrf, signal }),
  createExportDownloadGrant: (
    projectID: string,
    jobID: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<ExportGrant>(`${exportJobPath(projectID, jobID)}/download-grants`, {
      method: 'POST',
      csrf,
      signal,
    }),
  // Relative capability path; callers resolve it against their API origin and
  // fetch it directly with the one-time token, without session headers.
  exportDownloadPath: (token: string) => `/api/v1/exports/downloads/${encodeURIComponent(token)}`,
  listReports: (
    projectID: string,
    environment: ReportInput['environment'],
    page: { cursor?: string; limit?: number } = {},
    signal?: AbortSignal,
  ) =>
    request<ReportList>(
      `${projectPath(projectID)}/reports${catalogQuery({
        environment,
        cursor: page.cursor,
        limit: page.limit,
      })}`,
      { signal },
    ),
  createReport: (
    projectID: string,
    body: ReportInput,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<Report>(`${projectPath(projectID)}/reports`, {
      method: 'POST',
      body,
      csrf,
      signal,
      headers: { 'If-Match': '"0"', 'Idempotency-Key': idempotencyKey },
    }),
  getReport: (projectID: string, reportID: string, signal?: AbortSignal) =>
    request<Report>(`${projectPath(projectID)}/reports/${encodeURIComponent(reportID)}`, {
      signal,
    }),
  updateReport: (
    projectID: string,
    reportID: string,
    body: ReportInput,
    expectedRevision: number,
    idempotencyKey: string,
    csrf: string,
    signal?: AbortSignal,
  ) =>
    request<Report>(`${projectPath(projectID)}/reports/${encodeURIComponent(reportID)}`, {
      method: 'PATCH',
      body,
      csrf,
      signal,
      headers: {
        'If-Match': `"${expectedRevision}"`,
        'Idempotency-Key': idempotencyKey,
      },
    }),
  deleteReport: (projectID: string, reportID: string, csrf: string, signal?: AbortSignal) =>
    request<void>(`${projectPath(projectID)}/reports/${encodeURIComponent(reportID)}`, {
      method: 'DELETE',
      csrf,
      signal,
    }),
};
