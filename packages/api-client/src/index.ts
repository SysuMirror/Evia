import type { components } from './schema.gen';
export type { components, paths } from './schema.gen';
export type Session = components['schemas']['Session'];
export type Project = components['schemas']['Project'];
export type ProjectInput = components['schemas']['ProjectInput'];
export type ProjectList = components['schemas']['ProjectList'];

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
  options: { method?: string; body?: unknown; csrf?: string; signal?: AbortSignal } = {},
): Promise<T> {
  const headers = new Headers({ Accept: 'application/json' });
  if (options.body !== undefined) headers.set('Content-Type', 'application/json');
  if (options.csrf) headers.set('X-CSRF-Token', options.csrf);
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
};
