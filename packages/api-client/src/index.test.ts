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
