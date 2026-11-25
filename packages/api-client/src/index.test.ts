import assert from 'node:assert/strict';
import { afterEach, mock, test } from 'node:test';
import {
  api,
  ApiError,
  subscribeUnauthorized,
  subscribeProjectAccessDenied,
  type DashboardTextCard,
  type ErasureInput,
  type ExportInput,
  type SavedAnalysisExportInput,
  type MachineIssued,
  type MachinePrincipalInput,
  type MetricDefinition,
  type FormulaMetricDefinition,
} from './index.js';

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
  await api.getQueryResult('a/b', 'c/d', { signal: controller.signal });
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

test('query result continuation encodes cursor and page size', async () => {
  const fetch = respond(200, { query_id: 'q', columns: [], rows: [], metadata: {} });
  await api.getQueryResult('a/b', 'q');
  await api.getQueryResult('a/b', 'q', { limit: 100 });
  await api.getQueryResult('a/b', 'q', { cursor: 'next-token', limit: 100 });
  const calls = fetch.mock.calls;
  assert.equal(calls[0]?.arguments[0], '/api/v1/projects/a%2Fb/queries/q/result');
  assert.equal(calls[1]?.arguments[0], '/api/v1/projects/a%2Fb/queries/q/result?limit=100');
  assert.equal(
    calls[2]?.arguments[0],
    '/api/v1/projects/a%2Fb/queries/q/result?cursor=next-token&limit=100',
  );
});

test('analysis discovery, validation and journey routes encode scope and CSRF', async () => {
  const fetch = respond(200, {
    query_kinds: ['events', 'trend'],
    execution_available: true,
    environment: 'development',
    catalog_revision: 3,
    items: [],
    next_cursor: null,
  });
  const controller = new AbortController();
  await api.getAnalysisCapabilities('p/q', controller.signal);
  await api.getAnalysisCatalog(
    'p/q',
    { environment: 'development', limit: 50, cursor: 'a+/=' },
    controller.signal,
  );
  const calls = fetch.mock.calls;
  assert.equal(calls[0]?.arguments[0], '/api/v1/projects/p%2Fq/analysis-capabilities');
  assert.equal((calls[0]?.arguments[1] as RequestInit).method, 'GET');
  assert.equal(
    calls[1]?.arguments[0],
    '/api/v1/projects/p%2Fq/analysis-catalog?environment=development&cursor=a%2B%2F%3D&limit=50',
  );

  const body = {
    kind: 'trend',
    environment: 'development',
    time_range: { from: '2026-11-01T00:00:00Z', to: '2026-11-12T00:00:00Z' },
    timezone: 'UTC',
  };
  const writeFetch = respond(200, { valid: true, execution_performed: false, query_hash: 'h' });
  const validation = await api.validateQuery('p/q', body as never, 'csrf');
  assert.equal(validation.valid, true);
  const validationCall = writeFetch.mock.calls[0]!;
  assert.equal(validationCall.arguments[0], '/api/v1/projects/p%2Fq/query-validations');
  const validationInit = validationCall.arguments[1] as RequestInit;
  assert.equal(validationInit.method, 'POST');
  assert.equal((validationInit.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(validationInit.body as string), body);

  const journeyFetch = respond(202, { query_id: 'q-1', state: 'queued' });
  const job = await api.createJourneyQuery('p/q', body as never, 'csrf');
  assert.equal(job.query_id, 'q-1');
  const journeyCall = journeyFetch.mock.calls[0]!;
  assert.equal(journeyCall.arguments[0], '/api/v1/projects/p%2Fq/journeys/query');
  const journeyInit = journeyCall.arguments[1] as RequestInit;
  assert.equal(journeyInit.method, 'POST');
  assert.equal((journeyInit.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(journeyInit.body as string), body);
  controller.abort();
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

test('formula metric reads encode scope and paging', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  await api.listFormulaMetrics('p', { environment: 'development', limit: 20 });
  await api.getFormulaMetric('p', 'm/1');
  await api.getFormulaMetricDraft('p', 'm/1');
  await api.listFormulaMetricVersions('p', 'm/1', { limit: 10 });
  await api.getFormulaMetricVersion('p', 'm/1', 2);
  const calls = fetch.mock.calls;
  assert.equal(
    calls[0]?.arguments[0],
    '/api/v1/projects/p/formula-metrics?environment=development&limit=20',
  );
  assert.equal(calls[1]?.arguments[0], '/api/v1/projects/p/formula-metrics/m%2F1');
  assert.equal(calls[2]?.arguments[0], '/api/v1/projects/p/formula-metrics/m%2F1/draft');
  assert.equal(
    calls[3]?.arguments[0],
    '/api/v1/projects/p/formula-metrics/m%2F1/versions?limit=10',
  );
  assert.equal(calls[4]?.arguments[0], '/api/v1/projects/p/formula-metrics/m%2F1/versions/2');
  for (const call of calls) {
    assert.equal((call.arguments[1] as RequestInit).method, 'GET');
  }
});

test('formula metric writes send revision, CSRF and idempotency keys', async () => {
  const fetch = respond();
  const definition: FormulaMetricDefinition = {
    title: 'Lesson ratio',
    description: '',
    formula: {
      expression: {
        op: 'divide',
        left: {
          op: 'metric',
          metric: { metric_id: '11111111-1111-4111-8111-111111111111', version: 1 },
        },
        right: {
          op: 'metric',
          metric: { metric_id: '22222222-2222-4222-8222-222222222222', version: 1 },
        },
      },
      unit: 'number',
    },
  };
  await api.createFormulaMetric(
    'p',
    { environment: 'development', definition },
    'create-key',
    'csrf',
  );
  await api.updateFormulaMetricDraft('p', 'm', 3, { definition }, 'csrf');
  await api.validateFormulaMetric(
    'p',
    'm',
    { draft_revision: 4, expected_published_version: 0 },
    'csrf',
  );
  await api.publishFormulaMetric(
    'p',
    'm',
    { draft_revision: 4, expected_published_version: 0, validation_hash: 'a'.repeat(64) },
    'publish-key',
    'csrf',
  );
  const calls = fetch.mock.calls;
  const create = calls[0]?.arguments[1] as RequestInit;
  assert.equal(calls[0]?.arguments[0], '/api/v1/projects/p/formula-metrics');
  assert.equal(create.method, 'POST');
  assert.equal((create.headers as Headers).get('Idempotency-Key'), 'create-key');
  assert.equal((create.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(create.body as string), { environment: 'development', definition });
  const update = calls[1]?.arguments[1] as RequestInit;
  assert.equal(update.method, 'PATCH');
  assert.equal((update.headers as Headers).get('If-Match'), '"3"');
  assert.equal(calls[1]?.arguments[0], '/api/v1/projects/p/formula-metrics/m/draft');
  assert.equal(calls[2]?.arguments[0], '/api/v1/projects/p/formula-metrics/m/validations');
  const publish = calls[3]?.arguments[1] as RequestInit;
  assert.equal(publish.method, 'POST');
  assert.equal((publish.headers as Headers).get('Idempotency-Key'), 'publish-key');
  assert.equal(calls[3]?.arguments[0], '/api/v1/projects/p/formula-metrics/m/versions');
});

test('formula metric run enqueues a query job with idempotency key', async () => {
  const fetch = respond(202, { query_id: '77777777-7777-4777-8777-777777777777', state: 'queued' });
  const body = {
    time_range: { from: '2026-11-01T00:00:00Z', to: '2026-11-02T00:00:00Z' },
    timezone: 'UTC',
  };
  const job = await api.executeFormulaMetricVersion('p', 'm', 1, body, 'run-key', 'csrf');
  const call = fetch.mock.calls[0]!;
  assert.equal(call.arguments[0], '/api/v1/projects/p/formula-metrics/m/versions/1/queries');
  const init = call.arguments[1] as RequestInit;
  assert.equal(init.method, 'POST');
  assert.equal((init.headers as Headers).get('Idempotency-Key'), 'run-key');
  assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(init.body as string), body);
  assert.equal(job.state, 'queued');
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

test('saved analysis archive view encoding and lifecycle calls carry CSRF without body or key', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.listSavedAnalyses(
    'p/q',
    { environment: 'development', view: 'archived', limit: 20 },
    controller.signal,
  );
  await api.listSavedAnalyses(
    'p/q',
    { environment: 'development', view: 'active' },
    controller.signal,
  );
  await api.archiveSavedAnalysis('p/q', 'a/b', 'csrf', controller.signal);
  await api.unarchiveSavedAnalysis('p/q', 'a/b', 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/saved-analyses?environment=development&limit=20&view=archived',
      '/api/v1/projects/p%2Fq/saved-analyses?environment=development&view=active',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/archive',
      '/api/v1/projects/p%2Fq/saved-analyses/a%2Fb/unarchive',
    ],
  );
  for (const index of [2, 3]) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal(init.method, 'POST');
    assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
    assert.equal((init.headers as Headers).has('Idempotency-Key'), false);
    assert.equal(init.body, undefined);
    assert.equal(init.credentials, 'same-origin');
  }
  controller.abort();
  for (const call of fetch.mock.calls) {
    assert.equal((call.arguments[1] as RequestInit).signal?.aborted, true);
  }
});

test('saved analysis search term encodes as q alongside view and omits when blank', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listSavedAnalyses(
    'p/q',
    { environment: 'production', view: 'archived', search: '留存 分析', limit: 20 },
    controller.signal,
  );
  await api.listSavedAnalyses(
    'p/q',
    { environment: 'production', search: '100%_done' },
    controller.signal,
  );
  await api.listSavedAnalyses('p/q', { environment: 'production' }, controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/saved-analyses?environment=production&limit=20&view=archived&q=%E7%95%99%E5%AD%98+%E5%88%86%E6%9E%90',
      '/api/v1/projects/p%2Fq/saved-analyses?environment=production&q=100%25_done',
      '/api/v1/projects/p%2Fq/saved-analyses?environment=production',
    ],
  );
  controller.abort();
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

test('dashboard archive view encoding and lifecycle calls carry CSRF without body or key', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.listDashboards(
    'p/q',
    { environment: 'production', view: 'archived', limit: 20 },
    controller.signal,
  );
  await api.listDashboards('p/q', { environment: 'production', view: 'active' }, controller.signal);
  await api.archiveDashboard('p/q', 'd/r', 'csrf', controller.signal);
  await api.unarchiveDashboard('p/q', 'd/r', 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/dashboards?environment=production&limit=20&view=archived',
      '/api/v1/projects/p%2Fq/dashboards?environment=production&view=active',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/archive',
      '/api/v1/projects/p%2Fq/dashboards/d%2Fr/unarchive',
    ],
  );
  for (const index of [2, 3]) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal(init.method, 'POST');
    assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
    assert.equal((init.headers as Headers).has('Idempotency-Key'), false);
    assert.equal(init.body, undefined);
    assert.equal(init.credentials, 'same-origin');
  }
  controller.abort();
  for (const call of fetch.mock.calls)
    assert.equal((call.arguments[1] as RequestInit).signal?.aborted, true);
});

test('dashboard copy posts to the copy route with idempotency key and CSRF', async () => {
  const fetch = respond(201, { dashboard_id: 'new-dashboard' });
  const controller = new AbortController();
  await api.copyDashboard(
    'p/q',
    'd/r',
    { title: 'Copied dashboard', folder_id: 'f/1' },
    'copy-key',
    'csrf',
    controller.signal,
  );
  const [url, init] = fetch.mock.calls[0]!.arguments as [string, RequestInit];
  assert.equal(url, '/api/v1/projects/p%2Fq/dashboards/d%2Fr/copy');
  assert.equal(init.method, 'POST');
  assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.equal((init.headers as Headers).get('Idempotency-Key'), 'copy-key');
  assert.deepEqual(JSON.parse(init.body as string), {
    title: 'Copied dashboard',
    folder_id: 'f/1',
  });
  assert.equal(init.signal?.aborted, false);
  controller.abort();
  assert.equal(init.signal?.aborted, true);
});

test('dashboard cross-environment copy carries the target environment and rebindings', async () => {
  const fetch = respond(201, { dashboard_id: 'staging-dashboard' });
  await api.copyDashboard(
    'p/q',
    'd/r',
    {
      title: 'Staging copy',
      target_environment: 'staging',
      rebindings: [
        {
          widget_id: '11111111-1111-4111-8111-111111111111',
          analysis_id: '77777777-7777-4777-8777-777777777777',
          analysis_version: 4,
        },
        {
          widget_id: '22222222-2222-4222-8222-222222222222',
          metric_id: '88888888-8888-4888-8888-888888888888',
          metric_version: 5,
        },
      ],
    },
    'cross-key',
    'csrf',
  );
  const [url, init] = fetch.mock.calls[0]!.arguments as [string, RequestInit];
  assert.equal(url, '/api/v1/projects/p%2Fq/dashboards/d%2Fr/copy');
  assert.equal(init.method, 'POST');
  assert.deepEqual(JSON.parse(init.body as string), {
    title: 'Staging copy',
    target_environment: 'staging',
    rebindings: [
      {
        widget_id: '11111111-1111-4111-8111-111111111111',
        analysis_id: '77777777-7777-4777-8777-777777777777',
        analysis_version: 4,
      },
      {
        widget_id: '22222222-2222-4222-8222-222222222222',
        metric_id: '88888888-8888-4888-8888-888888888888',
        metric_version: 5,
      },
    ],
  });
});

test('dashboard text-card definitions ride draft payloads without an executable source', async () => {
  const fetch = respond(201, {});
  const controller = new AbortController();
  const textCard: DashboardTextCard = {
    content: '# 标题\n\n[文档](https://example.com)',
  };
  const definition = {
    title: '文本卡看板',
    widgets: [
      {
        widget_id: '11111111-1111-4111-8111-111111111111',
        title: '说明',
        reading_order: 0,
        layout: { x: 0, y: 0, width: 12, height: 3 },
        text_card: textCard,
      },
    ],
  };
  await api.createDashboard(
    'p/q',
    { environment: 'development', definition },
    'text-card-key',
    'csrf',
    controller.signal,
  );
  await api.updateDashboardDraft('p/q', 'd/r', 1, { definition }, 'csrf', controller.signal);
  assert.deepEqual(JSON.parse((fetch.mock.calls[0]!.arguments[1] as RequestInit).body as string), {
    environment: 'development',
    definition,
  });
  assert.deepEqual(JSON.parse((fetch.mock.calls[1]!.arguments[1] as RequestInit).body as string), {
    definition,
  });
  controller.abort();
});

test('dashboard folder scope encoding and folder CRUD calls', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listDashboards(
    'p/q',
    { environment: 'production', folderId: 'root', limit: 20 },
    controller.signal,
  );
  await api.listDashboards(
    'p/q',
    { environment: 'production', folderId: '66666666-6666-4666-8666-666666666666' },
    controller.signal,
  );
  await api.listDashboardFolders('p/q', { environment: 'production' }, controller.signal);
  await api.createDashboardFolder(
    'p/q',
    {
      environment: 'production',
      title: 'Forum',
      parent_id: '11111111-1111-4111-8111-111111111111',
    },
    'folder-key',
    'csrf',
    controller.signal,
  );
  await api.getDashboardFolder('p/q', 'f/r', controller.signal);
  await api.updateDashboardFolder('p/q', 'f/r', 2, { title: 'Renamed' }, 'csrf', controller.signal);
  await api.deleteDashboardFolder('p/q', 'f/r', 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/dashboards?environment=production&limit=20&folder_id=root',
      '/api/v1/projects/p%2Fq/dashboards?environment=production&folder_id=66666666-6666-4666-8666-666666666666',
      '/api/v1/projects/p%2Fq/dashboard-folders?environment=production',
      '/api/v1/projects/p%2Fq/dashboard-folders',
      '/api/v1/projects/p%2Fq/dashboard-folders/f%2Fr',
      '/api/v1/projects/p%2Fq/dashboard-folders/f%2Fr',
      '/api/v1/projects/p%2Fq/dashboard-folders/f%2Fr',
    ],
  );
  const createInit = fetch.mock.calls[3]!.arguments[1] as RequestInit;
  assert.equal((createInit.headers as Headers).get('Idempotency-Key'), 'folder-key');
  assert.deepEqual(JSON.parse(createInit.body as string), {
    environment: 'production',
    title: 'Forum',
    parent_id: '11111111-1111-4111-8111-111111111111',
  });
  const renameInit = fetch.mock.calls[5]!.arguments[1] as RequestInit;
  assert.equal((renameInit.headers as Headers).get('If-Match'), '"2"');
  assert.equal(renameInit.method, 'PATCH');
  const deleteInit = fetch.mock.calls[6]!.arguments[1] as RequestInit;
  assert.equal(deleteInit.method, 'DELETE');
  assert.equal(deleteInit.body, undefined);
  controller.abort();
});

test('dashboard search term encodes as q alongside other list predicates', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listDashboards(
    'p/q',
    { environment: 'production', search: '营收 看板', limit: 20 },
    controller.signal,
  );
  await api.listDashboards(
    'p/q',
    { environment: 'production', view: 'archived', folderId: 'root', search: '100%_done' },
    controller.signal,
  );
  await api.listDashboards('p/q', { environment: 'production' }, controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/dashboards?environment=production&limit=20&q=%E8%90%A5%E6%94%B6+%E7%9C%8B%E6%9D%BF',
      '/api/v1/projects/p%2Fq/dashboards?environment=production&view=archived&folder_id=root&q=100%25_done',
      '/api/v1/projects/p%2Fq/dashboards?environment=production',
    ],
  );
  controller.abort();
});

test('dashboard create and draft update carry the folder assignment', async () => {
  const fetch = respond();
  const controller = new AbortController();
  const definition = {
    title: 'Folded',
    description: '',
    widgets: [
      {
        widget_id: '11111111-1111-4111-8111-111111111111',
        analysis_id: '33333333-3333-4333-8333-333333333333',
        analysis_version: 1,
        title: 'Card',
        reading_order: 0,
        layout: { x: 0, y: 0, width: 12, height: 2 },
      },
    ],
  };
  await api.createDashboard(
    'p/q',
    { environment: 'production', folder_id: '66666666-6666-4666-8666-666666666666', definition },
    'create-key',
    'csrf',
    controller.signal,
  );
  await api.updateDashboardDraft(
    'p/q',
    'd/r',
    1,
    { definition, folder_id: undefined },
    'csrf',
    controller.signal,
  );
  assert.deepEqual(JSON.parse((fetch.mock.calls[0]!.arguments[1] as RequestInit).body as string), {
    environment: 'production',
    folder_id: '66666666-6666-4666-8666-666666666666',
    definition,
  });
  // Moving back to the root omits folder_id instead of sending null.
  assert.deepEqual(JSON.parse((fetch.mock.calls[1]!.arguments[1] as RequestInit).body as string), {
    definition,
  });
});

test('machine administration reads encode scope and allow only cursor and limit', async () => {
  const fetch = respond();
  const controller = new AbortController();
  // Project machine lists span environments; shared catalog scope must not leak in.
  const page = { environment: 'production' as const, cursor: 'a+/=', limit: 5 };
  await api.listMachinePrincipals('p/q', page, controller.signal);
  await api.listMachinePrincipals('p/q');
  await api.getMachinePrincipal('p/q', 'm/n', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/machine-principals?cursor=a%2B%2F%3D&limit=5',
      '/api/v1/projects/p%2Fq/machine-principals',
      '/api/v1/projects/p%2Fq/machine-principals/m%2Fn',
    ],
  );
  controller.abort();
  for (const index of [0, 2]) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.signal?.aborted, true);
    assert.equal((init.headers as Headers).has('Authorization'), false);
  }
});

test('content discovery canonicalizes types and encodes stream and item identifiers', async () => {
  const fetch = respond();
  const controller = new AbortController();
  const page = {
    environment: 'development' as const,
    contentTypes: ['session_replay_snapshot', 'session_replay_mutations'],
    cursor: 'a+/=',
    limit: 25,
  };
  await api.listContentStreams('p/q', page, controller.signal);
  await api.listContentItems('p/q', 'stream/one', page, controller.signal);
  const frozen = { ...page, cursor: undefined, asOf: '2026-11-23T04:00:00Z' };
  await api.listContentStreams('p/q', frozen, controller.signal);
  await api.listContentItems('p/q', 'stream/one', frozen, controller.signal);
  assert.throws(
    () => api.listContentStreams('p/q', { ...page, asOf: frozen.asOf }, controller.signal),
    /mutually exclusive/,
  );
  await api.getContentItem('p/q', 'item/one', 'development', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/content/streams?environment=development&content_types=session_replay_mutations%2Csession_replay_snapshot&cursor=a%2B%2F%3D&limit=25',
      '/api/v1/projects/p%2Fq/content/streams/stream%2Fone/items?environment=development&content_types=session_replay_mutations%2Csession_replay_snapshot&cursor=a%2B%2F%3D&limit=25',
      '/api/v1/projects/p%2Fq/content/streams?environment=development&content_types=session_replay_mutations%2Csession_replay_snapshot&as_of=2026-11-23T04%3A00%3A00Z&limit=25',
      '/api/v1/projects/p%2Fq/content/streams/stream%2Fone/items?environment=development&content_types=session_replay_mutations%2Csession_replay_snapshot&as_of=2026-11-23T04%3A00%3A00Z&limit=25',
      '/api/v1/projects/p%2Fq/content/items/item%2Fone?environment=development',
    ],
  );
  controller.abort();
  for (const call of fetch.mock.calls) {
    const init = call.arguments[1] as RequestInit;
    assert.equal(init.signal?.aborted, true);
    assert.equal(init.credentials, 'same-origin');
  }
});

test('machine writes preserve request bodies, retry keys, principal revisions and session CSRF', async () => {
  const fetch = respond();
  const controller = new AbortController();
  const create: MachinePrincipalInput = {
    name: 'Build agent',
    environment: 'staging',
    permissions: ['project.read', 'catalog.drafts.read', 'dashboards.run'],
    expires_at: '2026-11-10T12:00:00Z',
  };
  const rotation = { expires_at: '2026-11-11T12:00:00Z', overlap_seconds: 300 };
  await api.createMachinePrincipal('p/q', create, 'create-key', 'csrf', controller.signal);
  await api.rotateMachineCredential(
    'p/q',
    'm/n',
    7,
    rotation,
    'rotate-key',
    'csrf',
    controller.signal,
  );
  await api.revokeMachinePrincipal('p/q', 'm/n', 8, 'csrf', controller.signal);
  await api.revokeMachineCredential('p/q', 'm/n', 'c/d', 9, 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/machine-principals',
      '/api/v1/projects/p%2Fq/machine-principals/m%2Fn/credential-rotations',
      '/api/v1/projects/p%2Fq/machine-principals/m%2Fn',
      '/api/v1/projects/p%2Fq/machine-principals/m%2Fn/credentials/c%2Fd',
    ],
  );
  const bodies = [create, rotation, undefined, undefined];
  const keys = ['create-key', 'rotate-key', null, null];
  const revisions = [null, '"7"', '"8"', '"9"'];
  controller.abort();
  fetch.mock.calls.forEach((call, index) => {
    const init = call.arguments[1] as RequestInit;
    const headers = init.headers as Headers;
    assert.equal(init.method, index < 2 ? 'POST' : 'DELETE');
    assert.equal(init.credentials, 'same-origin');
    assert.equal(headers.get('X-CSRF-Token'), 'csrf');
    assert.equal(headers.get('Idempotency-Key'), keys[index]);
    assert.equal(headers.get('If-Match'), revisions[index]);
    assert.equal(headers.has('Authorization'), false);
    assert.deepEqual(
      init.body === undefined ? undefined : JSON.parse(init.body as string),
      bodies[index],
    );
    assert.equal(init.signal?.aborted, true);
  });
});

test('machine creation can retry an uncertain response with its original request and receive metadata only', async () => {
  const create: MachinePrincipalInput = {
    name: 'Build agent',
    environment: 'development',
    permissions: ['project.read'],
    expires_at: '2026-11-10T12:00:00Z',
  };
  const principal = {
    id: 'machine',
    project_id: 'project',
    environment: create.environment,
    name: create.name,
    permissions: create.permissions,
    revision: 1,
    created_at: '2026-11-09T12:00:00Z',
    revoked_at: null,
  };
  const replay: MachineIssued = {
    principal,
    credential: {
      id: 'credential',
      principal_id: principal.id,
      created_at: principal.created_at,
      expires_at: create.expires_at,
      revoked_at: null,
    },
    secret_available: false,
  };
  let attempt = 0;
  const fetch = mock.method(globalThis, 'fetch', async () => {
    attempt++;
    return new Response(attempt === 1 ? '{"principal":' : JSON.stringify(replay), { status: 201 });
  });
  await assert.rejects(
    api.createMachinePrincipal('project', create, 'same-key', 'csrf'),
    (error: unknown) => error instanceof ApiError && error.code === 'INVALID_RESPONSE',
  );
  const result = await api.createMachinePrincipal('project', create, 'same-key', 'csrf');
  assert.deepEqual(result, replay);
  assert.equal(result.secret_available, false);
  assert.equal(result.secret_once, undefined);
  const first = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  const retry = fetch.mock.calls[1]!.arguments[1] as RequestInit;
  assert.equal(retry.body, first.body);
  assert.equal(
    (retry.headers as Headers).get('Idempotency-Key'),
    (first.headers as Headers).get('Idempotency-Key'),
  );
});

test('governance reads encode scope and audit filters without leaking unknown params', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.listMembers('p/q', { cursor: 'a+/=', limit: 5 }, controller.signal);
  await api.getSettings('p/q', controller.signal);
  await api.listAuditLogs(
    'p/q',
    {
      from: '2026-11-01T00:00:00Z',
      to: '2026-11-08T00:00:00Z',
      action: 'content.item.read',
      actorID: 'a/b',
      environment: 'development',
      outcome: 'failure',
      resourceKeyPrefix: 'content.item?stream_id=replay%2F1&',
      cursor: 'c',
      limit: 50,
    },
    controller.signal,
  );
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/members?cursor=a%2B%2F%3D&limit=5',
      '/api/v1/projects/p%2Fq/settings',
      '/api/v1/projects/p%2Fq/audit-logs?from=2026-11-01T00%3A00%3A00Z&to=2026-11-08T00%3A00%3A00Z&action=content.item.read&actor_id=a%2Fb&environment=development&outcome=failure&resource_key_prefix=content.item%3Fstream_id%3Dreplay%252F1%26&cursor=c&limit=50',
    ],
  );
  controller.abort();
  for (const call of fetch.mock.calls) {
    const init = call.arguments[1] as RequestInit;
    assert.equal(init.method, 'GET');
    assert.equal(init.credentials, 'same-origin');
    assert.equal(init.signal?.aborted, true);
    assert.equal(init.body, undefined);
  }
});

test('governance writes pin revisions and idempotency while member removal sends no body', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.addMember(
    'p/q',
    3,
    { username: 'Editor.Name', role: 'editor' },
    'add-key',
    'csrf',
    controller.signal,
  );
  await api.updateMember(
    'p/q',
    'u/1',
    4,
    { role: 'viewer' },
    'role-key',
    'csrf',
    controller.signal,
  );
  await api.removeMember('p/q', 'u/1', 5, 'remove-key', 'csrf', controller.signal);
  await api.updateSettings(
    'p/q',
    2,
    { name: '论坛分析', timezone: 'Asia/Shanghai' },
    'settings-key',
    'csrf',
    controller.signal,
  );
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/members',
      '/api/v1/projects/p%2Fq/members/u%2F1',
      '/api/v1/projects/p%2Fq/members/u%2F1',
      '/api/v1/projects/p%2Fq/settings',
    ],
  );
  const expected = [
    {
      method: 'POST',
      revision: '"3"',
      key: 'add-key',
      body: { username: 'Editor.Name', role: 'editor' },
    },
    { method: 'PATCH', revision: '"4"', key: 'role-key', body: { role: 'viewer' } },
    { method: 'DELETE', revision: '"5"', key: 'remove-key', body: undefined },
    {
      method: 'PATCH',
      revision: '"2"',
      key: 'settings-key',
      body: { name: '论坛分析', timezone: 'Asia/Shanghai' },
    },
  ];
  controller.abort();
  fetch.mock.calls.forEach((call, index) => {
    const init = call.arguments[1] as RequestInit;
    const headers = init.headers as Headers;
    assert.equal(init.method, expected[index]!.method);
    assert.equal(headers.get('If-Match'), expected[index]!.revision);
    assert.equal(headers.get('Idempotency-Key'), expected[index]!.key);
    assert.equal(headers.get('X-CSRF-Token'), 'csrf');
    if (expected[index]!.body === undefined) {
      assert.equal(init.body, undefined);
      assert.equal(headers.has('Content-Type'), false);
    } else {
      assert.deepEqual(JSON.parse(init.body as string), expected[index]!.body);
    }
    assert.equal(init.signal?.aborted, true);
  });
});

test('metric reads encode environment scope and write calls pin revisions and idempotency', async () => {
  const fetch = respond();
  const controller = new AbortController();
  const definition: MetricDefinition = {
    title: '页面登录访客',
    event_versions: [{ event_key: 'page_opened', schema_version: 1 }],
    subject: 'uid',
    aggregation: { op: 'unique', accuracy: 'exact' },
    unit: 'raw_uid',
  };
  await api.listMetrics(
    'p/q',
    { environment: 'staging', cursor: 'c', limit: 5 },
    controller.signal,
  );
  await api.getMetric('p/q', 'm/1', controller.signal);
  await api.getMetricDraft('p/q', 'm/1', controller.signal);
  await api.listMetricVersions('p/q', 'm/1', { limit: 2 }, controller.signal);
  await api.createMetric(
    'p/q',
    { environment: 'development', definition },
    'create-key',
    'csrf',
    controller.signal,
  );
  await api.updateMetricDraft('p/q', 'm/1', 3, { definition }, 'csrf', controller.signal);
  await api.validateMetric(
    'p/q',
    'm/1',
    { draft_revision: 3, expected_published_version: 1 },
    'csrf',
    controller.signal,
  );
  await api.publishMetric(
    'p/q',
    'm/1',
    { draft_revision: 3, expected_published_version: 1, validation_hash: 'a'.repeat(64) },
    'publish-key',
    'csrf',
    controller.signal,
  );
  await api.executeMetricVersion(
    'p/q',
    'm/1',
    2,
    {
      time_range: { from: '2026-11-01T00:00:00Z', to: '2026-11-02T00:00:00Z' },
      timezone: 'UTC',
    },
    'run-key',
    'csrf',
    controller.signal,
  );
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/metrics?environment=staging&cursor=c&limit=5',
      '/api/v1/projects/p%2Fq/metrics/m%2F1',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/draft',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/versions?limit=2',
      '/api/v1/projects/p%2Fq/metrics',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/draft',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/validations',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/versions',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/versions/2/queries',
    ],
  );
  const draft = fetch.mock.calls[5]!.arguments[1] as RequestInit;
  assert.equal(draft.method, 'PATCH');
  assert.equal((draft.headers as Headers).get('If-Match'), '"3"');
  assert.equal((draft.headers as Headers).get('Idempotency-Key'), null);
  for (const [index, key] of [
    [4, 'create-key'],
    [7, 'publish-key'],
    [8, 'run-key'],
  ] as const) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal((init.headers as Headers).get('Idempotency-Key'), key);
    assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
  }
  controller.abort();
  for (const call of fetch.mock.calls)
    assert.equal((call.arguments[1] as RequestInit).signal?.aborted, true);
});

test('metric archive view encoding and lifecycle calls carry CSRF without body or key', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.listMetrics(
    'p/q',
    { environment: 'development', view: 'archived', limit: 20 },
    controller.signal,
  );
  await api.listMetrics('p/q', { environment: 'development', view: 'active' }, controller.signal);
  await api.archiveMetric('p/q', 'm/1', 'csrf', controller.signal);
  await api.unarchiveMetric('p/q', 'm/1', 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/metrics?environment=development&limit=20&view=archived',
      '/api/v1/projects/p%2Fq/metrics?environment=development&view=active',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/archive',
      '/api/v1/projects/p%2Fq/metrics/m%2F1/unarchive',
    ],
  );
  for (const index of [2, 3]) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal(init.method, 'POST');
    assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
    assert.equal((init.headers as Headers).has('Idempotency-Key'), false);
    assert.equal(init.body, undefined);
    assert.equal(init.credentials, 'same-origin');
  }
  controller.abort();
  for (const call of fetch.mock.calls)
    assert.equal((call.arguments[1] as RequestInit).signal?.aborted, true);
});

test('metric search term encodes as q alongside view and omits when blank', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listMetrics(
    'p/q',
    { environment: 'production', view: 'archived', search: '留存 指标', limit: 20 },
    controller.signal,
  );
  await api.listMetrics(
    'p/q',
    { environment: 'production', search: '100%_done' },
    controller.signal,
  );
  await api.listMetrics('p/q', { environment: 'production' }, controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/metrics?environment=production&limit=20&view=archived&q=%E7%95%99%E5%AD%98+%E6%8C%87%E6%A0%87',
      '/api/v1/projects/p%2Fq/metrics?environment=production&q=100%25_done',
      '/api/v1/projects/p%2Fq/metrics?environment=production',
    ],
  );
  controller.abort();
});

test('formula metric archive view encoding and lifecycle calls carry CSRF without body or key', async () => {
  const fetch = respond();
  const controller = new AbortController();
  await api.listFormulaMetrics(
    'p/q',
    { environment: 'development', view: 'archived', limit: 20 },
    controller.signal,
  );
  await api.listFormulaMetrics(
    'p/q',
    { environment: 'development', view: 'active' },
    controller.signal,
  );
  await api.archiveFormulaMetric('p/q', 'm/1', 'csrf', controller.signal);
  await api.unarchiveFormulaMetric('p/q', 'm/1', 'csrf', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/formula-metrics?environment=development&limit=20&view=archived',
      '/api/v1/projects/p%2Fq/formula-metrics?environment=development&view=active',
      '/api/v1/projects/p%2Fq/formula-metrics/m%2F1/archive',
      '/api/v1/projects/p%2Fq/formula-metrics/m%2F1/unarchive',
    ],
  );
  for (const index of [2, 3]) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal(init.method, 'POST');
    assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
    assert.equal((init.headers as Headers).has('Idempotency-Key'), false);
    assert.equal(init.body, undefined);
    assert.equal(init.credentials, 'same-origin');
  }
  controller.abort();
  for (const call of fetch.mock.calls)
    assert.equal((call.arguments[1] as RequestInit).signal?.aborted, true);
});

test('formula metric search term encodes as q alongside view and omits when blank', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listFormulaMetrics(
    'p/q',
    { environment: 'production', view: 'archived', search: '留存 公式', limit: 20 },
    controller.signal,
  );
  await api.listFormulaMetrics(
    'p/q',
    { environment: 'production', search: '100%_done' },
    controller.signal,
  );
  await api.listFormulaMetrics('p/q', { environment: 'production' }, controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/formula-metrics?environment=production&limit=20&view=archived&q=%E7%95%99%E5%AD%98+%E5%85%AC%E5%BC%8F',
      '/api/v1/projects/p%2Fq/formula-metrics?environment=production&q=100%25_done',
      '/api/v1/projects/p%2Fq/formula-metrics?environment=production',
    ],
  );
  controller.abort();
});

test('erasure jobs encode scope, idempotency keys and signed cursors', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  const input: ErasureInput = {
    environment: 'production',
    uid: 'user-to-erase',
    reason: 'customer right to erasure',
  };
  await api.createErasureJob('p/q', input, 'erase-key', 'csrf', controller.signal);
  await api.listErasureJobs('p/q', { cursor: 'a+/=', limit: 25 }, controller.signal);
  await api.listErasureJobs('p/q');
  await api.getErasureJob('p/q', '77777777-7777-4777-8777-777777777777', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/erasure-jobs',
      '/api/v1/projects/p%2Fq/erasure-jobs?cursor=a%2B%2F%3D&limit=25',
      '/api/v1/projects/p%2Fq/erasure-jobs',
      '/api/v1/projects/p%2Fq/erasure-jobs/77777777-7777-4777-8777-777777777777',
    ],
  );
  const post = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal(post.method, 'POST');
  assert.equal((post.headers as Headers).get('Idempotency-Key'), 'erase-key');
  assert.equal((post.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(post.body as string), input);
  controller.abort();
  for (const index of [0, 1, 3]) {
    const init = fetch.mock.calls[index]!.arguments[1] as RequestInit;
    assert.equal(init.signal?.aborted, true);
  }
});

test('export jobs encode scope, lifecycle actions and grant redemption path', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  // The generated inline query reference retains its JSON Schema $defs key;
  // the exported QueryRequest alias omits it, so cast the full wire document.
  const input = {
    name: 'weekly export',
    environment: 'production',
    query: {
      kind: 'trend',
      environment: 'production',
      time_range: { from: '2026-11-01T00:00:00Z', to: '2026-11-12T00:00:00Z' },
      timezone: 'UTC',
    },
  } as unknown as ExportInput;
  await api.createExportJob('p/q', input, 'export-key', 'csrf', controller.signal);
  await api.listExportJobs('p/q', { cursor: 'a+/=', limit: 25 }, controller.signal);
  await api.getExportJob('p/q', '88888888-8888-4888-8888-888888888888', controller.signal);
  await api.cancelExportJob(
    'p/q',
    '88888888-8888-4888-8888-888888888888',
    'csrf',
    controller.signal,
  );
  await api.createExportDownloadGrant(
    'p/q',
    '88888888-8888-4888-8888-888888888888',
    'csrf',
    controller.signal,
  );
  assert.equal(api.exportDownloadPath('tok en'), '/api/v1/exports/downloads/tok%20en');
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/exports',
      '/api/v1/projects/p%2Fq/exports?cursor=a%2B%2F%3D&limit=25',
      '/api/v1/projects/p%2Fq/exports/88888888-8888-4888-8888-888888888888',
      '/api/v1/projects/p%2Fq/exports/88888888-8888-4888-8888-888888888888',
      '/api/v1/projects/p%2Fq/exports/88888888-8888-4888-8888-888888888888/download-grants',
    ],
  );
  const post = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal(post.method, 'POST');
  assert.equal((post.headers as Headers).get('Idempotency-Key'), 'export-key');
  assert.equal((post.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(post.body as string), input);
  controller.abort();
});

test('saved analysis version exports post under the version resource with idempotency', async () => {
  const fetch = respond(201, {
    job_id: '88888888-8888-4888-8888-888888888888',
    project_id: 'p/q',
    environment: 'production',
    name: 'version export',
    format: 'csv',
    state: 'queued',
    error_code: null,
    result_rows: null,
    artifact_bytes: null,
    artifact_sha256: null,
    expires_at: null,
    created_at: '2026-11-19T00:00:00Z',
    updated_at: '2026-11-19T00:00:00Z',
    finished_at: null,
    source_analysis_id: '77777777-7777-4777-8777-777777777777',
    source_analysis_version: 3,
  });
  const controller = new AbortController();
  const body: SavedAnalysisExportInput = {
    name: 'version export',
    format: 'csv',
    time_range: { from: '2026-11-01T00:00:00Z', to: '2026-11-12T00:00:00Z' },
    timezone: 'UTC',
  };
  const job = await api.createSavedAnalysisExportJob(
    'p/q',
    '77777777-7777-4777-8777-777777777777',
    3,
    body,
    'saved-export-key',
    'csrf',
    controller.signal,
  );
  assert.equal(
    fetch.mock.calls[0]!.arguments[0],
    '/api/v1/projects/p%2Fq/saved-analyses/77777777-7777-4777-8777-777777777777/versions/3/exports',
  );
  const init = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal(init.method, 'POST');
  assert.equal((init.headers as Headers).get('Idempotency-Key'), 'saved-export-key');
  assert.equal((init.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(init.body as string), body);
  assert.equal(job.source_analysis_version, 3);
  controller.abort();
});

test('retention policy reads encode environment and writes send revision, CSRF and idempotency', async () => {
  const fetch = respond(200, {
    project_id: 'p',
    environment: 'production',
    retention_days: 365,
    revision: 2,
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-11T00:00:00Z',
  });
  const controller = new AbortController();
  await api.getRetentionPolicy('p', 'production', controller.signal);
  const body = { environment: 'production' as const, retention_days: 365 };
  await api.putRetentionPolicy('p', body, 2, 'retention-key', 'csrf', controller.signal);
  const calls = fetch.mock.calls;
  assert.equal(
    calls[0]?.arguments[0],
    '/api/v1/projects/p/retention-policy?environment=production',
  );
  assert.equal(calls[1]?.arguments[0], '/api/v1/projects/p/retention-policy');
  const put = calls[1]?.arguments[1] as RequestInit;
  assert.equal(put.method, 'PUT');
  assert.equal((put.headers as Headers).get('If-Match'), '"2"');
  assert.equal((put.headers as Headers).get('Idempotency-Key'), 'retention-key');
  assert.equal((put.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(put.body as string), body);
  controller.abort();
  for (const call of calls) {
    const init = call.arguments[1] as RequestInit;
    assert.equal(init.signal?.aborted, true);
  }
});

test('retention sweep reads encode environment and limit query params', async () => {
  const fetch = respond(200, {
    items: [
      {
        sweep_id: '33333333-3333-4333-8333-333333333333',
        project_id: 'p',
        environment: 'production',
        retention_days: 90,
        cutoff_at: '2026-08-13T10:00:00Z',
        dry_run: true,
        rows_candidates: 7,
        status: 'succeeded',
        detail: '',
        created_at: '2026-11-11T10:00:00Z',
        completed_at: '2026-11-11T10:00:00Z',
      },
    ],
  });
  const controller = new AbortController();
  await api.listRetentionSweeps('p', { environment: 'production', limit: 50 }, controller.signal);
  await api.listRetentionSweeps('p');
  const urls = fetch.mock.calls.map((call) => call.arguments[0]);
  assert.equal(urls[0], '/api/v1/projects/p/retention-sweeps?environment=production&limit=50');
  assert.equal(urls[1], '/api/v1/projects/p/retention-sweeps');
  const init = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal(init.method, 'GET');
  controller.abort();
  assert.equal(init.signal?.aborted, true);
});

test('archive manifest list and get encode scope and cursor', async () => {
  const fetch = respond(200, {
    items: [
      {
        manifest_id: '33333333-3333-4333-8333-333333333333',
        project_id: 'p',
        environment: 'production',
        retention_days: 90,
        cutoff_at: '2026-08-13T10:00:00Z',
        window_start_at: '2026-05-01T00:00:00Z',
        window_end_at: '2026-08-12T00:00:00Z',
        row_count: 128,
        artifact_path: '/var/lib/evia/archives/x.jsonl',
        artifact_sha256: null,
        artifact_bytes: 2048,
        status: 'materialized',
        error_code: null,
        sweep_id: null,
        created_at: '2026-08-13T10:05:00Z',
        completed_at: '2026-08-13T10:05:00Z',
      },
    ],
    next_cursor: null,
  });
  await api.listArchiveManifests('p', { environment: 'production', limit: 25, cursor: 'cur-1' });
  await api.listArchiveManifests('p');
  await api.getArchiveManifest('p', '33333333-3333-4333-8333-333333333333');
  const urls = fetch.mock.calls.map((call) => call.arguments[0]);
  assert.equal(
    urls[0],
    '/api/v1/projects/p/archive-manifests?environment=production&limit=25&cursor=cur-1',
  );
  assert.equal(urls[1], '/api/v1/projects/p/archive-manifests');
  assert.equal(
    urls[2],
    '/api/v1/projects/p/archive-manifests/33333333-3333-4333-8333-333333333333',
  );
  const init = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal(init.method, 'GET');
});

test('backfill jobs encode create, list, get and cancel paths', async () => {
  const fetch = respond(200, {
    job_id: '66666666-6666-4666-8666-666666666666',
    project_id: 'p',
    environment: 'development',
    manifest_id: '33333333-3333-4333-8333-333333333333',
    name: 'restore november',
    state: 'queued',
    rows_expected: 12,
    rows_inserted: 0,
    error_code: null,
    created_at: '2026-11-12T10:00:00Z',
    updated_at: '2026-11-12T10:00:00Z',
    finished_at: null,
  });
  const controller = new AbortController();
  const input = {
    name: 'restore november',
    environment: 'development' as const,
    manifest_id: '33333333-3333-4333-8333-333333333333',
  };
  await api.createBackfillJob('p', input, 'bf-key', 'csrf', controller.signal);
  await api.listBackfillJobs('p', { limit: 25 }, controller.signal);
  await api.getBackfillJob('p', '66666666-6666-4666-8666-666666666666', controller.signal);
  await api.cancelBackfillJob(
    'p',
    '66666666-6666-4666-8666-666666666666',
    'csrf',
    controller.signal,
  );
  const urls = fetch.mock.calls.map((call) => call.arguments[0]);
  assert.deepEqual(urls, [
    '/api/v1/projects/p/backfills',
    '/api/v1/projects/p/backfills?limit=25',
    '/api/v1/projects/p/backfills/66666666-6666-4666-8666-666666666666',
    '/api/v1/projects/p/backfills/66666666-6666-4666-8666-666666666666',
  ]);
  const post = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal(post.method, 'POST');
  assert.equal((post.headers as Headers).get('Idempotency-Key'), 'bf-key');
  assert.equal((post.headers as Headers).get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(post.body as string), input);
  const del = fetch.mock.calls[3]!.arguments[1] as RequestInit;
  assert.equal(del.method, 'DELETE');
  assert.equal((del.headers as Headers).get('X-CSRF-Token'), 'csrf');
});

test('retention policy create sends the expect-absent zero revision', async () => {
  const fetch = respond(200, {
    project_id: 'p',
    environment: 'development',
    retention_days: 90,
    revision: 1,
    created_at: null,
    updated_at: null,
  });
  const policy = await api.putRetentionPolicy(
    'p',
    { environment: 'development', retention_days: 90 },
    0,
    'create-key',
    'csrf',
  );
  const put = fetch.mock.calls[0]!.arguments[1] as RequestInit;
  assert.equal((put.headers as Headers).get('If-Match'), '"0"');
  assert.equal(policy.revision, 1);
  assert.equal(policy.retention_days, 90);
});

test('diagnostics read uses the fixed project diagnostics path', async () => {
  const fetch = respond(200, {
    project_id: 'p',
    generated_at: '2026-11-12T10:00:00Z',
    features: {
      collection_enabled: true,
      analysis_enabled: true,
      delivery_enabled: false,
    },
    sources: {
      status: 'ok',
      total: 1,
      truncated: false,
      by_status: [{ name: 'active', count: 1 }],
      by_environment: [{ name: 'production', count: 1 }],
      disabled_sources: [],
    },
    budget: {
      status: 'disabled',
      rate_per_second: 0,
      burst: 0,
      environments: [],
    },
    pipeline: { status: 'no_data', proofs: [] },
    rejections: {
      status: 'disabled',
      window_hours: 24,
      totals: [],
      windows: [],
    },
  });
  const controller = new AbortController();
  const report = await api.getDiagnostics('p', controller.signal);
  const [url, init] = fetch.mock.calls[0]!.arguments;
  assert.equal(url, '/api/v1/projects/p/diagnostics');
  assert.equal((init as RequestInit).method, 'GET');
  assert.equal(report.sources.total, 1);
  assert.equal(report.pipeline.status, 'no_data');
  controller.abort();
  assert.equal((init as RequestInit).signal?.aborted, true);
});

test('project overview read encodes the bounded aggregate scope', async () => {
  const fetch = respond(200, {
    project_id: 'p',
    environment: 'development',
    from: '2026-11-21T00:00:00Z',
    to: '2026-11-22T00:00:00Z',
    interval: 'hour',
    total_events: 4,
    unique_sessions: 2,
    identified_users: 2,
    anonymous_ids: 3,
    data_watermark: '2026-11-21T23:00:00Z',
    trend: [{ bucket: '2026-11-21T00:00:00Z', events: 4, sessions: 2 }],
    top_events: [{ event_key: 'sys.page_view', count: 4 }],
    metadata: {
      accuracy: 'exact',
      coverage: 'provisional',
      known_gaps: ['pipeline_completeness_not_established'],
      generated_at: '2026-11-22T00:00:01Z',
    },
  });
  const controller = new AbortController();
  const result = await api.getProjectOverview(
    'p',
    {
      environment: 'development',
      from: '2026-11-21T00:00:00Z',
      to: '2026-11-22T00:00:00Z',
      interval: 'hour',
    },
    controller.signal,
  );
  const [url, init] = fetch.mock.calls[0]!.arguments;
  assert.equal(
    url,
    '/api/v1/projects/p/overview?environment=development&from=2026-11-21T00%3A00%3A00Z&to=2026-11-22T00%3A00%3A00Z&interval=hour',
  );
  assert.equal((init as RequestInit).method, 'GET');
  assert.equal(result.total_events, 4);
  assert.equal(result.metadata.accuracy, 'exact');
  controller.abort();
  assert.equal((init as RequestInit).signal?.aborted, true);
});

test('collection quota read and patch encode scope, revision and idempotency', async () => {
  const fetch = respond(200, {
    project_id: 'p',
    environment: 'production',
    rate_per_second: 5,
    burst: 60,
    override: true,
    source: 'project_override',
    revision: 2,
    runtime_status: 'applied',
    created_at: '2026-11-12T10:00:00Z',
    updated_at: '2026-11-12T11:00:00Z',
  });
  const controller = new AbortController();
  const input = { environment: 'production' as const, rate_per_second: 5, burst: 60 };
  await api.getCollectionQuota('p', 'production', controller.signal);
  const quota = await api.patchCollectionQuota(
    'p',
    input,
    2,
    'quota-key',
    'csrf',
    controller.signal,
  );
  const urls = fetch.mock.calls.map((call) => call.arguments[0]);
  assert.equal(urls[0], '/api/v1/projects/p/quotas?environment=production');
  assert.equal(urls[1], '/api/v1/projects/p/quotas');
  const patchInit = fetch.mock.calls[1]!.arguments[1] as RequestInit;
  assert.equal(patchInit.method, 'PATCH');
  const headers = patchInit.headers as Headers;
  assert.equal(headers.get('If-Match'), '"2"');
  assert.equal(headers.get('Idempotency-Key'), 'quota-key');
  assert.equal(headers.get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(patchInit.body as string), input);
  assert.equal(quota.runtime_status, 'applied');
  controller.abort();
  assert.equal(patchInit.signal?.aborted, true);
});

test('release typescript artifact read encodes the nested release path', async () => {
  const fetch = respond(200, {
    release_id: '33333333-3333-4333-8333-333333333333',
    environment: 'production',
    release_revision: 2,
    manifest_hash: 'a'.repeat(64),
    language: 'typescript',
    language_version: '5.0',
    event_count: 1,
    byte_size: 256,
    checksum: 'b'.repeat(64),
    compatibility: [
      {
        event_key: 'lesson_opened',
        schema_version: 1,
        interface_name: 'LessonOpenedProperties',
        valid_identifier: true,
        unsupported_constructs: [],
      },
    ],
    source: 'export interface LessonOpenedProperties {\n  lesson_id: number;\n}\n',
  });
  const controller = new AbortController();
  const artifact = await api.getReleaseTypeScriptArtifact(
    'p',
    '33333333-3333-4333-8333-333333333333',
    controller.signal,
  );
  const [url, init] = fetch.mock.calls[0]!.arguments;
  assert.equal(
    url,
    '/api/v1/projects/p/releases/33333333-3333-4333-8333-333333333333/artifacts/typescript',
  );
  assert.equal((init as RequestInit).method, 'GET');
  assert.equal(artifact.compatibility[0]!.interface_name, 'LessonOpenedProperties');
  controller.abort();
  assert.equal((init as RequestInit).signal?.aborted, true);
});

test('reports lifecycle encodes scope, revision, idempotency and cursor', async () => {
  const report = {
    report_id: '66666666-6666-4666-8666-666666666666',
    project_id: 'p',
    environment: 'development',
    revision: 1,
    definition: {
      title: '周报',
      description: '合成说明',
      sections: [
        {
          section_id: '77777777-7777-4777-8777-777777777777',
          reading_order: 1,
          section_title: '第一章',
          analysis_id: '44444444-4444-4444-8444-444444444444',
          analysis_version: 1,
        },
      ],
    },
    created_at: '2026-11-12T00:00:00Z',
    updated_at: '2026-11-12T00:00:00Z',
  };
  const fetch = respond(200, report);
  const input = {
    environment: 'development' as const,
    definition: {
      title: '周报',
      description: '合成说明',
      sections: [
        {
          reading_order: 1,
          section_title: '第一章',
          analysis_id: '44444444-4444-4444-8444-444444444444',
          analysis_version: 1,
        },
      ],
    },
  };
  await api.listReports('p', 'development', { cursor: 'cur', limit: 20 });
  await api.createReport('p', input, 'create-key', 'csrf');
  await api.getReport('p', report.report_id);
  const updated = await api.updateReport('p', report.report_id, input, 2, 'update-key', 'csrf');
  const urls = fetch.mock.calls.map((call) => call.arguments[0]);
  assert.equal(urls[0], '/api/v1/projects/p/reports?environment=development&cursor=cur&limit=20');
  assert.equal(urls[1], '/api/v1/projects/p/reports');
  assert.equal(urls[2], '/api/v1/projects/p/reports/66666666-6666-4666-8666-666666666666');
  assert.equal(urls[3], '/api/v1/projects/p/reports/66666666-6666-4666-8666-666666666666');
  const postInit = fetch.mock.calls[1]!.arguments[1] as RequestInit;
  assert.equal(postInit.method, 'POST');
  const postHeaders = postInit.headers as Headers;
  assert.equal(postHeaders.get('If-Match'), '"0"');
  assert.equal(postHeaders.get('Idempotency-Key'), 'create-key');
  const patchInit = fetch.mock.calls[3]!.arguments[1] as RequestInit;
  assert.equal(patchInit.method, 'PATCH');
  const patchHeaders = patchInit.headers as Headers;
  assert.equal(patchHeaders.get('If-Match'), '"2"');
  assert.equal(patchHeaders.get('Idempotency-Key'), 'update-key');
  assert.equal(patchHeaders.get('X-CSRF-Token'), 'csrf');
  assert.deepEqual(JSON.parse(patchInit.body as string), input);
  assert.equal(updated.report_id, report.report_id);
});

test('report search term encodes as q and omits when blank', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listReports('p/q', 'production', { limit: 20, search: '留存 报告' }, controller.signal);
  await api.listReports('p/q', 'production', { search: '100%_done' }, controller.signal);
  await api.listReports('p/q', 'production', {}, controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/reports?environment=production&limit=20&q=%E7%95%99%E5%AD%98+%E6%8A%A5%E5%91%8A',
      '/api/v1/projects/p%2Fq/reports?environment=production&q=100%25_done',
      '/api/v1/projects/p%2Fq/reports?environment=production',
    ],
  );
  controller.abort();
});

test('report delete sends CSRF and accepts an empty 204 body', async () => {
  const fetch = mock.method(
    globalThis,
    'fetch',
    async () => new Response(null, { status: 204, headers: { 'X-Request-ID': 'request-1' } }),
  );
  const result = await api.deleteReport('p', '66666666-6666-4666-8666-666666666666', 'csrf');
  assert.equal(result, undefined);
  const [url, init] = fetch.mock.calls[0]!.arguments;
  assert.equal(url, '/api/v1/projects/p/reports/66666666-6666-4666-8666-666666666666');
  assert.equal((init as RequestInit).method, 'DELETE');
  assert.equal(((init as RequestInit).headers as Headers).get('X-CSRF-Token'), 'csrf');
});

test('account profile and password routes send CSRF and expected payloads', async () => {
  const fetch = respond(200, {
    user_id: 'u-1',
    username: 'alice',
    display_name: 'Alice Chen',
    created_at: '2026-11-12T00:00:00Z',
    csrf_token: 'rotated-csrf',
    expires_at: '2026-11-19T00:00:00Z',
  });
  const controller = new AbortController();
  await api.getAccountProfile(controller.signal);
  await api.updateAccountProfile({ display_name: 'Alice Chen' }, 'csrf-1', controller.signal);
  await api.changeAccountPassword(
    { current_password: 'old-password-1234', new_password: 'new-password-5678' },
    'csrf-1',
    controller.signal,
  );
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    ['/api/v1/account', '/api/v1/account/profile', '/api/v1/account/password'],
  );
  assert.equal((fetch.mock.calls[0]!.arguments[1] as RequestInit).method, 'GET');
  const patchInit = fetch.mock.calls[1]!.arguments[1] as RequestInit;
  assert.equal(patchInit.method, 'PATCH');
  assert.equal((patchInit.headers as Headers).get('X-CSRF-Token'), 'csrf-1');
  assert.deepEqual(JSON.parse(patchInit.body as string), { display_name: 'Alice Chen' });
  const pwInit = fetch.mock.calls[2]!.arguments[1] as RequestInit;
  assert.equal(pwInit.method, 'POST');
  assert.equal((pwInit.headers as Headers).get('X-CSRF-Token'), 'csrf-1');
  assert.deepEqual(JSON.parse(pwInit.body as string), {
    current_password: 'old-password-1234',
    new_password: 'new-password-5678',
  });
  controller.abort();
  assert.equal(pwInit.signal?.aborted, true);
});

test('project invitation management and public acceptance routes encode scope and optional CSRF', async () => {
  const fetch = respond(200, { items: [], next_cursor: null });
  const controller = new AbortController();
  await api.listProjectInvitations('p/q', { cursor: 'cur+1', limit: 20 }, controller.signal);
  await api.createProjectInvitation(
    'p/q',
    { role: 'editor', note: 'Frontend', expires_in_hours: 168 },
    'csrf-1',
    controller.signal,
  );
  await api.revokeProjectInvitation('p/q', 'inv/1', 'csrf-1', controller.signal);
  await api.previewInvitation({ token: 'tok-secret-123456' }, controller.signal);
  await api.acceptInvitation(
    {
      token: 'tok-secret-123456',
      username: 'newuser',
      display_name: 'New User',
      password: 'long-password-1234',
    },
    undefined,
    controller.signal,
  );
  await api.acceptInvitation({ token: 'tok-secret-123456' }, 'csrf-existing', controller.signal);
  assert.deepEqual(
    fetch.mock.calls.map((call) => call.arguments[0]),
    [
      '/api/v1/projects/p%2Fq/invitations?cursor=cur%2B1&limit=20',
      '/api/v1/projects/p%2Fq/invitations',
      '/api/v1/projects/p%2Fq/invitations/inv%2F1',
      '/api/v1/invitations/preview',
      '/api/v1/invitations/accept',
      '/api/v1/invitations/accept',
    ],
  );
  const createInit = fetch.mock.calls[1]!.arguments[1] as RequestInit;
  assert.equal(createInit.method, 'POST');
  assert.equal((createInit.headers as Headers).get('X-CSRF-Token'), 'csrf-1');
  const revokeInit = fetch.mock.calls[2]!.arguments[1] as RequestInit;
  assert.equal(revokeInit.method, 'DELETE');
  assert.equal((revokeInit.headers as Headers).get('X-CSRF-Token'), 'csrf-1');
  const anonAcceptInit = fetch.mock.calls[4]!.arguments[1] as RequestInit;
  assert.equal((anonAcceptInit.headers as Headers).get('X-CSRF-Token'), null);
  const authAcceptInit = fetch.mock.calls[5]!.arguments[1] as RequestInit;
  assert.equal((authAcceptInit.headers as Headers).get('X-CSRF-Token'), 'csrf-existing');
  controller.abort();
  assert.equal(authAcceptInit.signal?.aborted, true);
});
