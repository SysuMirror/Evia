import assert from 'node:assert/strict';
import { afterEach, mock, test } from 'node:test';
import { api, ApiError, subscribeUnauthorized, subscribeProjectAccessDenied } from './index.js';

afterEach(() => mock.restoreAll());

function respond(status = 200, body: unknown = {}) {
  return mock.method(
    globalThis,
    'fetch',
    async () =>
      new Response(JSON.stringify(body), { status, headers: { 'X-Request-ID': 'request-1' } }),
  );
}

test('scoped reads encode identifiers and preserve cancellation', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.getProject('a/b', controller.signal);
  await api.getActivation('a/b', 'production', controller.signal);
  await api.getEventVersion('a/b', 'opened', 2, controller.signal);
  await api.getQueryResult('a/b', 'c/d', controller.signal);
  const calls = fetch.mock.calls;
  assert.equal(calls[0]?.arguments[0], '/api/v1/projects/a%2Fb');
  assert.equal(
    calls[1]?.arguments[0],
    '/api/v1/projects/a%2Fb/release-activations?environment=production',
  );
  assert.equal(
    calls[2]?.arguments[0],
    '/api/v1/projects/a%2Fb/event-definitions/opened/versions/2',
  );
  assert.equal(calls[3]?.arguments[0], '/api/v1/projects/a%2Fb/queries/c%2Fd/result');
  controller.abort();
  for (const call of calls) {
    const init = call.arguments[1] as RequestInit;
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.signal?.aborted, true);
  }
});

test('source updates and release activation send revision, CSRF and idempotency', async () => {
  const fetch = respond();
  await api.updateSource('p', 's', 4, { status: 'disabled' }, 'csrf');
  await api.rotateCredential('p', 's', { overlap_seconds: 0 }, 'rotation-key', 'csrf');
  await api.activateRelease(
    'p',
    { environment: 'production', release_id: 'r', expected_active_revision: 0 },
    'activation-key',
    'csrf',
  );
  const calls = fetch.mock.calls;
  const update = calls[0]?.arguments[1] as RequestInit;
  assert.equal(update.method, 'PATCH');
  assert.equal((update.headers as Headers).get('If-Match'), '"4"');
  assert.equal((update.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(update.body as string), { status: 'disabled' });
  assert.equal(
    ((calls[1]?.arguments[1] as RequestInit).headers as Headers).get('Idempotency-Key'),
    'rotation-key',
  );
  assert.equal(
    ((calls[2]?.arguments[1] as RequestInit).headers as Headers).get('Idempotency-Key'),
    'activation-key',
  );
});

test('imperative unauthorized calls notify subscribers and retain safe error details', async () => {
  respond(401, {
    code: 'SESSION_EXPIRED',
    message: '请登录',
    details: { field_paths: ['/schema'] },
  });
  let notifications = 0;
  const unsubscribe = subscribeUnauthorized(() => {
    notifications++;
  });
  try {
    await assert.rejects(
      api.createSource(
        'p',
        {
          name: 'Example',
          kind: 'browser',
          environment: 'development',
          allowed_origins: ['https://example.com'],
        },
        'csrf',
      ),
      (error: unknown) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.requestID, 'request-1');
        assert.deepEqual(error.details?.field_paths, ['/schema']);
        return true;
      },
    );
    assert.equal(notifications, 1);
    await assert.rejects(api.login({ username: 'example', password: 'wrong' }));
    assert.equal(notifications, 1);
    unsubscribe();
    await assert.rejects(api.getProject('p'));
    assert.equal(notifications, 1);
  } finally {
    unsubscribe();
  }
});

test('caller cancellation retains its error instead of reporting a network failure', async () => {
  const controller = new AbortController();
  const canceled = new DOMException('Canceled', 'AbortError');
  controller.abort();
  mock.method(globalThis, 'fetch', async () => {
    throw canceled;
  });
  await assert.rejects(api.getQuery('p', 'q', controller.signal), (error) => error === canceled);
});

test('imperative project denials notify synchronously with decoded scope and can unsubscribe', async () => {
  const events: string[] = [];
  const unsubscribe = subscribeProjectAccessDenied((projectID) => {
    events.push(projectID);
  });
  try {
    respond(403, { code: 'PROJECT_FORBIDDEN', message: '权限已变更' });
    await assert.rejects(
      api.updateSource('project/one', 'source', 1, { status: 'disabled' }, 'csrf'),
      (error: unknown) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.code, 'PROJECT_FORBIDDEN');
        assert.deepEqual(events, ['project/one']);
        return true;
      },
    );
    mock.restoreAll();
    respond(404, { code: 'PROJECT_NOT_FOUND', message: '不可见' });
    await assert.rejects(api.getProject('project two'), (error: unknown) => {
      assert.ok(error instanceof ApiError);
      assert.deepEqual(events, ['project/one', 'project two']);
      return true;
    });
    await assert.rejects(api.getActivation('project three', 'development'));
    assert.deepEqual(events, ['project/one', 'project two', 'project three']);
    unsubscribe();
    await assert.rejects(api.getQuery('project four', 'missing-query'));
    assert.equal(events.length, 3);
  } finally {
    unsubscribe();
  }
});

test('resource errors and unscoped paths never signal project access denial', async () => {
  const projects: string[] = [];
  const unsubscribe = subscribeProjectAccessDenied((projectID) => {
    projects.push(projectID);
  });
  try {
    for (const error of [
      { status: 404, code: 'QUERY_NOT_FOUND' },
      { status: 404, code: 'SOURCE_NOT_FOUND' },
      { status: 404, code: 'EVENT_VERSION_NOT_FOUND' },
      { status: 403, code: 'CSRF_REQUIRED' },
      { status: 403, code: 'ORIGIN_FORBIDDEN' },
      { status: 401, code: 'SESSION_EXPIRED' },
    ]) {
      mock.restoreAll();
      respond(error.status, { code: error.code, message: '请求失败' });
      await assert.rejects(api.getQuery('project', 'query'));
    }
    for (const code of ['PROJECT_NOT_FOUND', 'PROJECT_FORBIDDEN']) {
      mock.restoreAll();
      respond(404, { code, message: '不可见' });
      await assert.rejects(api.projects());
      await assert.rejects(api.createProject({ name: 'Example', timezone: 'UTC' }, 'csrf'));
      await assert.rejects(api.session());
      await assert.rejects(api.login({ username: 'example', password: 'wrong' }));
    }
    assert.deepEqual(projects, []);
  } finally {
    unsubscribe();
  }
});

test('successful response body failures retain uncertain outcome and safe request metadata', async () => {
  const privateContent = 'private upstream response';
  for (const failure of ['malformed JSON', 'stream failure', 'timeout after headers']) {
    mock.restoreAll();
    const headers = { 'X-Request-ID': 'committed-request' };
    mock.method(globalThis, 'fetch', async () => {
      if (failure === 'malformed JSON') {
        return new Response(`{"private":"${privateContent}"`, { status: 201, headers });
      }
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new TextEncoder().encode(`{"private":"${privateContent}"`));
        },
        pull(controller) {
          controller.error(
            failure === 'timeout after headers'
              ? new DOMException(privateContent, 'TimeoutError')
              : new TypeError(privateContent),
          );
        },
      });
      return new Response(body, { status: 201, headers });
    });
    await assert.rejects(
      api.createRelease(
        'project',
        {
          environment: 'development',
          expected_active_revision: 0,
          events: [{ event_key: 'opened', draft_revision: 1 }],
        },
        'stable-release-key',
        'csrf',
      ),
      (error: unknown) => {
        assert.ok(error instanceof ApiError, failure);
        assert.equal(error.status, 0, failure);
        assert.equal(error.code, 'INVALID_RESPONSE', failure);
        assert.equal(error.requestID, 'committed-request', failure);
        assert.equal(error.message, '服务响应未完整接收，请重新加载确认操作结果。', failure);
        assert.equal(error.details, undefined, failure);
        assert.equal(error.message.includes(privateContent), false, failure);
        return true;
      },
    );
  }
});

test('explicit caller cancellation during successful response body read preserves its error', async () => {
  const controller = new AbortController();
  const canceled = new DOMException('Canceled after headers', 'AbortError');
  mock.method(globalThis, 'fetch', async () => {
    const response = new Response('{}', { status: 200 });
    mock.method(response, 'json', async () => {
      controller.abort();
      throw canceled;
    });
    return response;
  });
  await assert.rejects(api.getProject('project', controller.signal), (error) => error === canceled);
});

test('empty success responses do not attempt JSON decoding', async () => {
  mock.method(globalThis, 'fetch', async () => new Response(null, { status: 204 }));
  assert.equal(await api.logout('csrf'), undefined);
});

test('saved analysis reads encode resource scope, cursors, versions and preserve cancellation', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.listSavedAnalyses(
    'p/q',
    { environment: 'development', cursor: 'a+/=', limit: 3 },
    controller.signal,
  );
  await api.getSavedAnalysis('p/q', 'a/b', controller.signal);
  await api.getSavedAnalysisDraft('p/q', 'a/b', controller.signal);
  await api.listSavedAnalysisVersions(
    'p/q',
    'a/b',
    { cursor: 'v+/=', limit: 2 },
    controller.signal,
  );
  await api.getSavedAnalysisVersion('p/q', 'a/b', 7, controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/saved-analyses?environment=development&cursor=a%2B%2F%3D&limit=3',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/draft',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/versions?cursor=v%2B%2F%3D&limit=2',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/versions/7',
    ],
  );
  controller.abort();
  for (const call of fetch.mock.calls) {
    const init = call.arguments[1] as RequestInit;
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.signal?.aborted, true);
  }
});

const savedDefinition = {
  title: 'Daily opens',
  description: '',
  query: {
    kind: 'trend',
    event_versions: [{ event_key: 'page_opened', schema_version: 1 }],
    metrics: [{ op: 'count' }],
  },
  visualization: { kind: 'metric' },
} as const;

// Typed request fixtures exercise client transport; no mocked response proves
// publication, durable idempotency or query execution.
test('saved analysis writes preserve body, revisions, CSRF, idempotency and cancellation', async () => {
  const fetch = respond();
  const controller = new AbortController();
  const definition = {
    ...savedDefinition,
    query: {
      ...savedDefinition.query,
      event_versions: [...savedDefinition.query.event_versions],
      metrics: [...savedDefinition.query.metrics],
    },
  };
  const create = { environment: 'development' as const, definition };
  const validation = { draft_revision: 4, expected_published_version: 2 };
  const publication = { ...validation, validation_hash: 'a'.repeat(64) };
  const execution = {
    time_range: { from: '2026-11-01T00:00:00Z', to: '2026-11-02T00:00:00Z' },
    timezone: 'UTC',
  };
  await api.createSavedAnalysis('p/q', create, 'create-key', 'csrf', controller.signal);
  await api.updateSavedAnalysisDraft('p/q', 'a/b', 4, { definition }, 'csrf', controller.signal);
  await api.validateSavedAnalysis('p/q', 'a/b', validation, 'csrf', controller.signal);
  await api.publishSavedAnalysis(
    'p/q',
    'a/b',
    publication,
    'publish-key',
    'csrf',
    controller.signal,
  );
  await api.executeSavedAnalysisVersion(
    'p/q',
    'a/b',
    2,
    execution,
    'execute-key',
    'csrf',
    controller.signal,
  );
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/saved-analyses',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/draft',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/validations',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/versions',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/versions/2/queries',
    ],
  );
  const bodies = [create, { definition }, validation, publication, execution];
  const keys = ['create-key', null, null, 'publish-key', 'execute-key'];
  controller.abort();
  fetch.mock.calls.forEach((call, index) => {
    const init = call.arguments[1] as RequestInit;
    const headers = init.headers as Headers;
    assert.equal(init.method, index === 1 ? 'PATCH' : 'POST');
    assert.equal(headers.get('X-CSRF-Token'), 'csrf');
    assert.equal(headers.get('Idempotency-Key'), keys[index]);
    assert.equal(headers.get('If-Match'), index === 1 ? '"4"' : null);
    assert.deepEqual(JSON.parse(init.body as string), bodies[index]);
    assert.equal(init.signal?.aborted, true);
  });
});

test('saved analysis requests reuse safe errors, project denial signals and explicit cancellation', async () => {
  let denied = '';
  const unsubscribe = subscribeProjectAccessDenied((projectID) => {
    denied = projectID;
  });
  try {
    respond(403, {
      code: 'PROJECT_FORBIDDEN',
      message: '无权访问',
      details: { field_paths: ['/project_id'] },
    });
    await assert.rejects(api.getSavedAnalysisDraft('p/q', 'analysis'), (error: unknown) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.code, 'PROJECT_FORBIDDEN');
      assert.equal(error.requestID, 'request-1');
      assert.deepEqual(error.details?.field_paths, ['/project_id']);
      assert.equal(denied, 'p/q');
      return true;
    });
    mock.restoreAll();
    respond(409, { code: 'SAVED_ANALYSIS_REVISION_CONFLICT', message: '草稿已更新' });
    await assert.rejects(
      api.validateSavedAnalysis(
        'p',
        'a',
        { draft_revision: 1, expected_published_version: 0 },
        'csrf',
      ),
      (error: unknown) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 409);
        assert.equal(error.code, 'SAVED_ANALYSIS_REVISION_CONFLICT');
        return true;
      },
    );
    mock.restoreAll();
    const controller = new AbortController();
    const canceled = new DOMException('Canceled', 'AbortError');
    controller.abort();
    mock.method(globalThis, 'fetch', async () => {
      throw canceled;
    });
    await assert.rejects(
      api.getSavedAnalysisVersion('p', 'a', 1, controller.signal),
      (error) => error === canceled,
    );
  } finally {
    unsubscribe();
  }
});

test('dashboard transport pins versions, targets filters, and preserves recovery headers', async () => {
  const fetch = respond();
  const controller = new AbortController();
  const definition = {
    title: 'Dashboard',
    widgets: [
      {
        widget_id: 'widget',
        analysis_id: 'analysis',
        analysis_version: 2,
        title: 'Chart',
        reading_order: 0,
        layout: { x: 0, y: 0, width: 6, height: 4 },
      },
    ],
  };
  const counters = { draft_revision: 3, expected_published_version: 1 };
  const execute = {
    time_range: { from: '2026-11-01T00:00:00.123456789Z', to: '2026-11-02T00:00:00.123456789Z' },
    timezone: 'UTC',
    widget_filters: [
      {
        widget_id: 'widget',
        filter: {
          op: 'eq' as const,
          field: { kind: 'system' as const, key: 'event_key' as const },
          value: 'opened',
        },
      },
    ],
  };
  await api.listDashboards(
    'p/q',
    { environment: 'production', cursor: 'a b', limit: 7 },
    controller.signal,
  );
  await api.getDashboardDraft('p/q', 'd/r', controller.signal);
  await api.createDashboard(
    'p/q',
    { environment: 'production', definition },
    'create-key',
    'csrf',
    controller.signal,
  );
  await api.updateDashboardDraft('p/q', 'd/r', 3, { definition }, 'csrf', controller.signal);
  await api.validateDashboard('p/q', 'd/r', counters, 'csrf', controller.signal);
  await api.publishDashboard(
    'p/q',
    'd/r',
    { ...counters, validation_hash: 'hash' },
    'publish-key',
    'csrf',
    controller.signal,
  );
  await api.executeDashboardVersion('p/q', 'd/r', 2, execute, 'run-key', 'csrf', controller.signal);
  await api.getDashboardRun('p/q', 'r/s', controller.signal);
  await api.cancelDashboardRun('p/q', 'r/s', 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/dashboards?environment=production&cursor=a+b&limit=7',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/draft',
      '/api/v1/projects/p%2Fq/dashboards',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/draft',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/validations',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/versions',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/versions/2/runs',
      '/api/v1/projects/p%2Fq/dashboard-runs/r%2Fs',
      '/api/v1/projects/p%2Fq/dashboard-runs/r%2Fs',
    ],
  );
  const update = fetch.mock.calls[3]!.arguments[1] as RequestInit;
  assert.equal((update.headers as Headers).get('If-Match'), '"3"');
  for (const [index, key] of [
    [2, 'create-key'],
    [5, 'publish-key'],
    [6, 'run-key'],
  ] as const) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal((init.headers as Headers).get('Idempotency-Key'), key);
    assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
  }
  assert.deepEqual(
    JSON.parse((fetch.mock.calls[6]!.arguments[1] as RequestInit).body as string),
    execute,
  );
  assert.equal((fetch.mock.calls[8]!.arguments[1] as RequestInit).method, 'DELETE');
  controller.abort();
  for (const call of fetch.mock.calls)
    assert.equal((call.arguments[1] as RequestInit).signal?.aborted, true);
});
