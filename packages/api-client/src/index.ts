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
// The generator exposes JSON Schema's annotation as a property; it is not wire data.
export type QueryRequest = Omit<components['schemas']['query-request.schema'], '$defs'>;
export type QueryJob = components['schemas']['query-job.schema'];
export type QueryResult = components['schemas']['query-result.schema'];
export type Environment = Release['environment'];
export type CatalogPage = { environment?: Environment; cursor?: string; limit?: number };

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
function catalogQuery(page: CatalogPage): string {
  const query = new URLSearchParams();
  if (page.environment !== undefined) query.set('environment', page.environment);
  if (page.cursor !== undefined) query.set('cursor', page.cursor);
  if (page.limit !== undefined) query.set('limit', String(page.limit));
  return query.size ? `?${query.toString()}` : '';
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
  getQueryResult: (projectID: string, queryID: string, signal?: AbortSignal) =>
    request<QueryResult>(
      `${projectPath(projectID)}/queries/${encodeURIComponent(queryID)}/result`,
      { signal },
    ),
};
