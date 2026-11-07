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
export type Environment = Release['environment'];
export type CatalogPage = { environment?: Environment; cursor?: string; limit?: number };

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestID?: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
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
    let problem: Partial<components['schemas']['Problem']> = {};
    try {
      problem = await response.json();
    } catch {
      /* Reverse proxies may return non-JSON failures. */
    }
    throw new ApiError(
      response.status,
      typeof problem.code === 'string' ? problem.code : 'HTTP_ERROR',
      typeof problem.message === 'string' ? problem.message : `请求失败（${response.status}）`,
      response.headers.get('X-Request-ID') ?? undefined,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
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
};
