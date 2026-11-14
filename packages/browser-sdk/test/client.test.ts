import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createEviaClient, EviaSDKError } from '../src/index.js';
import type { ClientOptions, Clock, PublicConfig } from '../src/index.js';

const projectId = '00000000-0000-4000-8000-000000000001';
const receiptId = '00000000-0000-4000-8000-000000000002';
const etag = `"${'a'.repeat(64)}"`;
const config = (revision = 1): PublicConfig => ({
  format: 'evia.public-config.v1',
  project_id: projectId,
  environment: 'development',
  release_revision: revision,
  events: [
    {
      event_key: 'page_opened',
      schema_version: revision,
      schema: { type: 'object', additionalProperties: true },
    },
    { event_key: 'item_opened', schema_version: 1, schema: { type: 'object' } },
  ],
});
const json = (value: unknown, status = 200, headers: HeadersInit = {}): Response =>
  new Response(JSON.stringify(value), { status, headers });
interface Body {
  batch_id: string;
  events: Record<string, any>[];
}
const receipt = (body: Body, results?: unknown[], status = 202, headers?: HeadersInit): Response =>
  json(
    {
      batch_id: body.batch_id,
      receipt_id: receiptId,
      results:
        results ??
        body.events.map((event, index) => ({
          index,
          event_id: event.event_id,
          status: 'accepted',
        })),
    },
    status,
    headers,
  );
const pump = async (): Promise<void> => {
  for (let i = 0; i < 30; i++) await Promise.resolve();
};

class TestClock implements Clock {
  time = Date.parse('2026-11-09T12:00:00Z');
  id = 0;
  tasks = new Map<number, { due: number; callback: () => void }>();
  now = (): number => this.time;
  setTimeout = (callback: () => void, delay: number): ReturnType<typeof setTimeout> => {
    const id = ++this.id;
    this.tasks.set(id, { due: this.time + delay, callback });
    return id as unknown as ReturnType<typeof setTimeout>;
  };
  clearTimeout = (timer: ReturnType<typeof setTimeout>): void => {
    this.tasks.delete(timer as unknown as number);
  };
  async advance(milliseconds: number): Promise<void> {
    const until = this.time + milliseconds;
    let turns = 0;
    while (true) {
      const first = [...this.tasks].sort((a, b) => a[1].due - b[1].due)[0];
      if (!first || first[1].due > until) break;
      assert.ok(++turns < 1000, 'timer work must remain bounded');
      this.time = first[1].due;
      this.tasks.delete(first[0]);
      first[1].callback();
      await pump();
    }
    this.time = until;
    await pump();
  }
}

function setup(
  handler: (body: Body, init: RequestInit) => Response | Promise<Response>,
  extra: Partial<ClientOptions> = {},
) {
  const clock = new TestClock();
  let id = 100;
  const bodies: string[] = [];
  const gets: { url: string; init: RequestInit }[] = [];
  const fetcher: typeof fetch = async (input, init = {}) => {
    if (String(input).includes('/v1/config')) {
      gets.push({ url: String(input), init });
      return json(config(), 200, { ETag: etag });
    }
    bodies.push(String(init.body));
    return handler(JSON.parse(String(init.body)) as Body, init);
  };
  const client = createEviaClient({
    endpoint: 'https://events.example',
    writeKey: 'public-example',
    projectId,
    environment: 'development',
    fetch: fetcher,
    clock,
    randomUUID: () => `00000000-0000-4000-8000-${String(++id).padStart(12, '0')}`,
    storage: null,
    lifecycle: false,
    requestTimeoutMs: 100,
    flushIntervalMs: 10,
    retryBaseMs: 10,
    ...extra,
  });
  return { client, clock, bodies, gets };
}

test('SSR import and bounded custom emission use published configuration without credentials', async () => {
  const { client, bodies, gets } = setup((body, init) => {
    assert.equal(init.credentials, 'omit');
    assert.equal(init.referrerPolicy, 'no-referrer');
    assert.equal(init.redirect, 'error');
    assert.equal(
      (init.headers as Record<string, string>)['Content-Type'],
      'text/plain;charset=UTF-8',
    );
    return receipt(body);
  });
  const properties = { label: 'first', values: [0, false, ''] };
  const emission = client.emit('page_opened', properties);
  properties.label = 'changed';
  properties.values.push('late');
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  const body = JSON.parse(bodies[0]!) as Body;
  assert.deepEqual(body.events[0]!.properties, { label: 'first', values: [0, false, ''] });
  assert.equal(body.events[0]!.schema_version, 1);
  assert.equal(body.events[0]!.release_revision, 1);
  assert.equal(gets.length, 1);
  assert.equal(gets[0]!.init.credentials, 'omit');
  assert.equal(client.pending, 0);
  assert.throws(
    () => client.emit('sys.page_view'),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_CUSTOM_EVENT_KEY',
  );
  const absent = client.emit('unknown_event');
  assert.equal((await absent.result).code, 'EVENT_NOT_PUBLISHED');
  client.destroy();
});

test('uncertain retries keep identical batch bytes, event identity, time and revision', async () => {
  let calls = 0;
  const { client, bodies, clock } = setup((body) => {
    if (++calls === 1) throw new TypeError('connection interrupted');
    return receipt(body);
  });
  await client.init();
  client.identify('first-user');
  const emission = client.emit('page_opened', { score: 1.5 });
  await client.flush();
  assert.equal(client.pending, 1);
  client.identify('second-user');
  client.reset();
  await clock.advance(10);
  assert.equal((await emission.result).status, 'accepted');
  assert.equal(bodies.length, 2);
  assert.equal(bodies[0], bodies[1]);
  assert.equal(JSON.parse(bodies[1]!).events[0].identity.uid, 'first-user');
  client.destroy();
});

test('partial receipts retry only retryable events and retain their envelopes', async () => {
  let calls = 0;
  const { client, bodies, clock } = setup((body) => {
    if (++calls === 1)
      return receipt(
        body,
        [
          { index: 0, event_id: body.events[0]!.event_id, status: 'accepted' },
          {
            index: 1,
            event_id: body.events[1]!.event_id,
            status: 'rejected',
            error: { code: 'PROPERTY_SCHEMA_MISMATCH', retryable: false },
          },
          {
            index: 2,
            event_id: body.events[2]!.event_id,
            status: 'rejected',
            error: { code: 'SCHEMA_NOT_READY', retryable: true },
          },
        ],
        207,
      );
    return receipt(body);
  });
  await client.init();
  const emissions = [
    client.emit('page_opened', { value: 0 }),
    client.emit('page_opened', { value: 1 }),
    client.emit('page_opened', { value: 2 }),
  ];
  await client.flush();
  assert.equal((await emissions[0]!.result).status, 'accepted');
  assert.equal((await emissions[1]!.result).status, 'rejected');
  await clock.advance(10);
  assert.equal((await emissions[2]!.result).status, 'accepted');
  const first = JSON.parse(bodies[0]!) as Body;
  const second = JSON.parse(bodies[1]!) as Body;
  assert.notEqual(first.batch_id, second.batch_id);
  assert.deepEqual(second.events, [first.events[2]]);
  client.destroy();
});

test('Retry-After prevents early retry and bounded attempts do not imply delivery', async () => {
  const { client, clock, bodies } = setup((body) =>
    bodies.length === 1
      ? new Response(null, { status: 429, headers: { 'Retry-After': '2' } })
      : receipt(body),
  );
  await client.init();
  const emission = client.emit('page_opened');
  await client.flush();
  await clock.advance(1999);
  assert.equal(bodies.length, 1);
  await clock.advance(1);
  assert.equal((await emission.result).status, 'accepted');
  client.destroy();

  const second = setup(() => new Response(null, { status: 503 }), { maxAttempts: 2 });
  await second.client.init();
  const pending = second.client.emit('page_opened');
  await second.client.flush();
  await second.clock.advance(10);
  assert.equal((await pending.result).status, 'uncertain');
  assert.equal(second.bodies.length, 2);
  second.client.destroy();
});

test('malformed or mismatched receipts cannot silently accept any events', async () => {
  const { client, clock, bodies } = setup(
    (body) => receipt(body, [{ index: 0, event_id: receiptId, status: 'accepted' }]),
    { maxAttempts: 2 },
  );
  await client.init();
  const emission = client.emit('page_opened');
  await client.flush();
  await clock.advance(10);
  assert.equal((await emission.result).status, 'uncertain');
  assert.equal(bodies[0], bodies[1]);
  client.destroy();
});

test('backpressure and strict values reject before allocating unbounded queue work', async () => {
  const { client } = setup((body) => receipt(body), { maxQueueEvents: 2 });
  await client.init();
  const first = client.emit('page_opened');
  const second = client.emit('page_opened');
  assert.throws(() => client.emit('page_opened'), /QUEUE_FULL/);
  assert.equal(client.pending, 2);
  client.disable();
  assert.equal((await first.result).status, 'dropped');
  assert.equal((await second.result).status, 'dropped');
  await client.enable();
  for (const value of [
    NaN,
    Infinity,
    Number.MAX_SAFE_INTEGER + 1,
    null,
    { nested: true },
    [null],
    Array(21).fill(1),
  ]) {
    assert.throws(() => client.emit('page_opened', { value } as never), /INVALID_PROPERTY_VALUE/);
  }
  assert.throws(() => client.emit('page_opened', { 'bad.key': 1 }), /INVALID_PROPERTY_KEY/);
  assert.throws(() => client.emit('page_opened', { value: 'x'.repeat(1025) }), /INVALID_TEXT/);
  const accessors = Object.defineProperty({}, 'value', {
    enumerable: true,
    get() {
      throw new Error('must not invoke accessor');
    },
  });
  assert.throws(() => client.emit('page_opened', accessors), /INVALID_PROPERTIES/);
  client.destroy();
});

test('navigation tokens join explicit source and destination without inventing page events', async () => {
  const { client, bodies } = setup((body) => receipt(body));
  await client.init();
  const initial = client.navigate({
    routeName: 'Home',
    routeTemplate: '/',
    navigationType: 'initial',
  });
  const link = client.beginNavigation();
  const source = client.emit(
    'item_opened',
    { item_id: 'one' },
    { navigation: link, operationId: 'open-one' },
  );
  const destination = client.navigate({ routeName: 'Item', routeTemplate: '/items/:id', link });
  const page = client.emit('page_opened');
  assert.throws(
    () => client.emit('item_opened', {}, { navigation: link }),
    /INVALID_NAVIGATION_LINK/,
  );
  await client.flush();
  assert.equal((await source.result).status, 'accepted');
  await page.result;
  const events = (JSON.parse(bodies[0]!) as Body).events;
  assert.equal(events.length, 2);
  assert.equal(events[0]!.context.page_view_id, initial.page_view_id);
  assert.equal(events[0]!.context.navigation_id, link.navigationId);
  assert.equal(events[1]!.context.navigation_id, link.navigationId);
  assert.equal(events[1]!.context.source_page_view_id, initial.page_view_id);
  assert.equal(events[1]!.context.previous_page_view_id, initial.page_view_id);
  assert.equal(events[1]!.context.page_view_id, destination.page_view_id);
  assert.equal(events[1]!.context.sequence, events[0]!.context.sequence + 1);
  assert.equal('url' in events[1]!.context, false);
  client.destroy();
});

test('identity reset and inactivity preserve earlier snapshots and prevent account linking', async () => {
  const { client, bodies, clock } = setup((body) => receipt(body), { sessionTimeoutMs: 60000 });
  await client.init();
  const anonymous = client.emit('page_opened');
  client.identify('user-one');
  const identified = client.emit('page_opened');
  client.reset();
  const reset = client.emit('page_opened');
  await client.flush();
  await Promise.all([anonymous.result, identified.result, reset.result]);
  const events = (JSON.parse(bodies[0]!) as Body).events;
  assert.equal(events[0]!.identity.uid, undefined);
  assert.equal(events[1]!.identity.uid, 'user-one');
  assert.equal(events[0]!.identity.anonymous_id, events[1]!.identity.anonymous_id);
  assert.notEqual(events[1]!.identity.anonymous_id, events[2]!.identity.anonymous_id);
  assert.notEqual(events[1]!.context.session_id, events[2]!.context.session_id);
  assert.equal(events[2]!.identity.uid, undefined);
  await clock.advance(60000);
  const afterIdle = client.emit('page_opened');
  await pump();
  await client.flush();
  await afterIdle.result;
  const later = JSON.parse(bodies[1]!).events[0];
  assert.notEqual(later.context.session_id, events[2]!.context.session_id);
  assert.equal(later.identity.anonymous_id, events[2]!.identity.anonymous_id);
  client.destroy();
});

test('single-flight requests settle on timeout even when custom fetch ignores abort', async () => {
  const { client, clock, bodies } = setup(() => new Promise<Response>(() => {}), {
    maxAttempts: 1,
  });
  await client.init();
  const emission = client.emit('page_opened');
  const first = client.flush();
  const second = client.flush();
  assert.equal(first, second);
  await pump();
  assert.equal(bodies.length, 1);
  await clock.advance(100);
  await first;
  assert.equal((await emission.result).status, 'uncertain');
  assert.equal(client.pending, 0);
  client.destroy();
  assert.equal(clock.tasks.size, 0);
});

test('disable aborts requests and late responses cannot mutate a newly enabled generation', async () => {
  let complete!: (response: Response) => void;
  const { client, bodies } = setup(
    () =>
      new Promise<Response>((resolve) => {
        complete = resolve;
      }),
  );
  await client.init();
  const old = client.emit('page_opened');
  const flushing = client.flush();
  await pump();
  client.disable();
  assert.equal((await old.result).status, 'uncertain');
  await client.enable();
  const next = client.emit('page_opened');
  complete(receipt(JSON.parse(bodies[0]!)));
  await flushing;
  await pump();
  assert.equal(client.pending, 1);
  client.destroy();
  assert.equal((await next.result).status, 'dropped');
  assert.throws(() => client.emit('page_opened'), /CLIENT_DESTROYED/);
});

test('bounded request batches drain in order and accepted means queue acknowledgement', async () => {
  const { client, bodies } = setup((body) => receipt(body), { maxBatchEvents: 2 });
  await client.init();
  const emissions = Array.from({ length: 5 }, (_, value) => client.emit('page_opened', { value }));
  await client.flush();
  const outcomes = await Promise.all(emissions.map((emission) => emission.result));
  assert.deepEqual(
    bodies.map((body) => JSON.parse(body).events.length),
    [2, 2, 1],
  );
  assert.ok(
    outcomes.every((outcome) => outcome.status === 'accepted' && outcome.receiptId === receiptId),
  );
  assert.ok(bodies.every((body) => new TextEncoder().encode(body).byteLength <= 60 * 1024));
  client.destroy();
});

test('configuration TTL and authorized ETag refresh preserve immutable emitted revisions', async () => {
  const calls: { url: string; init: RequestInit }[] = [];
  const bodies: Body[] = [];
  const { client, clock } = setup(
    () => {
      throw new Error('unused');
    },
    {
      configTtlMs: 1000,
      fetch: async (input, init = {}) => {
        if (String(input).includes('/v1/collect')) {
          const body = JSON.parse(String(init.body)) as Body;
          bodies.push(body);
          return receipt(body);
        }
        calls.push({ url: String(input), init });
        if (calls.length === 1) return json(config(), 200, { ETag: etag });
        if (calls.length === 2) return new Response(null, { status: 304 });
        return json(config(2));
      },
    },
  );
  const initial = await client.init();
  assert.ok(Object.isFrozen(initial));
  assert.ok(Object.isFrozen(initial.events[0]!.schema));
  await client.init();
  assert.equal(calls.length, 1);
  await clock.advance(1000);
  await Promise.all([client.init(), client.init()]);
  assert.equal(calls.length, 2);
  assert.equal(new URL(calls[1]!.url).searchParams.get('current_release_revision'), '1');
  assert.equal((calls[1]!.init.headers as Record<string, string>)['If-None-Match'], etag);
  const first = client.emit('page_opened');
  await client.refreshConfig();
  const second = client.emit('page_opened');
  await client.flush();
  await Promise.all([first.result, second.result]);
  assert.deepEqual(
    bodies[0]!.events.map((event) => event.release_revision),
    [1, 2],
  );
  assert.deepEqual(
    bodies[0]!.events.map((event) => event.schema_version),
    [1, 2],
  );
  client.destroy();
});

test('expired configuration failure and foreign scope fail closed without collection', async () => {
  let configs = 0;
  const { client, clock, bodies } = setup((body) => receipt(body), {
    configTtlMs: 1000,
    fetch: async (input) => {
      assert.ok(String(input).includes('/v1/config'));
      return ++configs === 1 ? json(config()) : new Response(null, { status: 403 });
    },
  });
  await client.init();
  await clock.advance(1000);
  const emission = client.emit('page_opened');
  assert.equal((await emission.result).code, 'CONFIG_HTTP_403');
  assert.equal(client.pending, 0);
  assert.equal(bodies.length, 0);
  assert.equal(clock.tasks.size, 0);
  client.destroy();
  const foreign = setup(
    () => {
      throw new Error('unused');
    },
    { fetch: async () => json({ ...config(), project_id: receiptId }) },
  );
  await assert.rejects(foreign.client.init(), /INVALID_CONFIG/);
  const event = foreign.client.emit('page_opened');
  assert.equal((await event.result).status, 'dropped');
  foreign.client.destroy();
});

test('queue byte, event byte and UTF-8 limits remain bounded independently of event count', async () => {
  const { client } = setup((body) => receipt(body), { maxQueueBytes: 8192 });
  await client.init();
  const properties = Object.fromEntries(
    Array.from({ length: 4 }, (_, index) => [`value_${index}`, 'x'.repeat(1024)]),
  );
  const emission = client.emit('page_opened', properties);
  assert.throws(() => client.emit('page_opened', properties), /QUEUE_FULL/);
  assert.equal(client.pending, 1);
  client.disable();
  await emission.result;
  await client.enable();
  assert.throws(
    () =>
      client.emit('page_opened', {
        one: '雪'.repeat(1024),
        two: '雪'.repeat(1024),
        three: '雪'.repeat(1024),
      }),
    /EVENT_TOO_LARGE/,
  );
  assert.equal(client.pending, 0);
  client.destroy();
});

test('event age expires bounded work instead of ignoring long Retry-After', async () => {
  const { client, clock, bodies } = setup(
    () => new Response(null, { status: 429, headers: { 'Retry-After': '3600' } }),
    { maxAgeMs: 1000 },
  );
  await client.init();
  const emission = client.emit('page_opened');
  await client.flush();
  await clock.advance(1000);
  const expired = await emission.result;
  assert.equal(expired.code, 'EVENT_EXPIRED');
  assert.equal(expired.status, 'rejected');
  assert.equal(bodies.length, 1);
  assert.equal(client.pending, 0);
  client.destroy();

  const pendingConfig = setup(
    () => {
      throw new Error('unused');
    },
    { maxAgeMs: 1000, requestTimeoutMs: 5000, fetch: async () => new Promise<Response>(() => {}) },
  );
  const waiting = pendingConfig.client.emit('page_opened');
  await pendingConfig.clock.advance(1000);
  assert.equal((await waiting.result).status, 'dropped');
  pendingConfig.client.destroy();
});

test('lifecycle flush uses one bounded keepalive batch and destroy removes listeners', async () => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  const browserWindow = new EventTarget();
  const browserDocument = Object.assign(new EventTarget(), { visibilityState: 'hidden' });
  Object.defineProperty(globalThis, 'window', { configurable: true, value: browserWindow });
  Object.defineProperty(globalThis, 'document', { configurable: true, value: browserDocument });
  let calls = 0;
  const { client } = setup(
    (body, init) => {
      calls++;
      assert.equal(init.keepalive, true);
      return receipt(body);
    },
    { lifecycle: true },
  );
  try {
    await client.init();
    const emission = client.emit('page_opened');
    await pump();
    browserWindow.dispatchEvent(new Event('pagehide'));
    await pump();
    assert.equal((await emission.result).status, 'accepted');
    assert.equal(calls, 1);
    client.destroy();
    browserWindow.dispatchEvent(new Event('pagehide'));
    browserDocument.dispatchEvent(new Event('visibilitychange'));
    await pump();
    assert.equal(calls, 1);
  } finally {
    client.destroy();
    if (previousWindow) Object.defineProperty(globalThis, 'window', previousWindow);
    else Reflect.deleteProperty(globalThis, 'window');
    if (previousDocument) Object.defineProperty(globalThis, 'document', previousDocument);
    else Reflect.deleteProperty(globalThis, 'document');
  }
});

test('all-rejected 422 receipts retain individual errors and receipt identifiers', async () => {
  const { client } = setup((body) =>
    receipt(
      body,
      body.events.map((event, index) => ({
        index,
        event_id: event.event_id,
        status: 'rejected',
        error: { code: 'PROPERTY_SCHEMA_MISMATCH', retryable: false },
      })),
      422,
    ),
  );
  await client.init();
  const emission = client.emit('page_opened', { unsupported_value: true });
  await client.flush();
  const outcome = await emission.result;
  assert.equal(outcome.status, 'rejected');
  assert.equal(outcome.code, 'PROPERTY_SCHEMA_MISMATCH');
  assert.equal(outcome.receiptId, receiptId);
  assert.equal(outcome.attempts, 1);
  client.destroy();
});

test('invalid raw UID cannot poison valid neighboring events or change earlier identity', async () => {
  const { client, bodies } = setup((body) => receipt(body));
  await client.init();
  client.identify('valid-user');
  const first = client.emit('page_opened');
  assert.throws(() => client.identify('invalid\0user'), /INVALID_UID/);
  const second = client.emit('page_opened');
  await client.flush();
  assert.ok(
    (await Promise.all([first.result, second.result])).every(
      (outcome) => outcome.status === 'accepted',
    ),
  );
  assert.deepEqual(
    JSON.parse(bodies[0]!).events.map((event: Record<string, any>) => event.identity.uid),
    ['valid-user', 'valid-user'],
  );
  client.destroy();
});

test('uppercase project UUIDs use the canonical configuration and storage scope', async () => {
  const canonical = 'abcdefab-0000-4000-8000-000000000001';
  const keys: string[] = [];
  const { client } = setup(
    () => {
      throw new Error('unused');
    },
    {
      projectId: canonical.toUpperCase(),
      storage: {
        getItem: () => null,
        setItem: (key) => {
          keys.push(key);
        },
      },
      fetch: async () => json({ ...config(), project_id: canonical }),
    },
  );
  assert.equal((await client.init()).project_id, canonical);
  assert.ok(keys[0]!.includes(canonical));
  client.destroy();
});

test('an ordinary in-flight request remains keepalive-capable during pagehide', async () => {
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  const browserWindow = new EventTarget();
  Object.defineProperty(globalThis, 'window', { configurable: true, value: browserWindow });
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: Object.assign(new EventTarget(), { visibilityState: 'visible' }),
  });
  let complete!: (response: Response) => void;
  const { client, bodies } = setup(
    (_body, init) => {
      assert.equal(init.keepalive, true);
      return new Promise<Response>((resolve) => {
        complete = resolve;
      });
    },
    { lifecycle: true },
  );
  try {
    await client.init();
    const emission = client.emit('page_opened');
    const flight = client.flush();
    await pump();
    browserWindow.dispatchEvent(new Event('pagehide'));
    await pump();
    assert.equal(bodies.length, 1);
    complete(receipt(JSON.parse(bodies[0]!)));
    await flight;
    assert.equal((await emission.result).status, 'accepted');
  } finally {
    client.destroy();
    if (previousWindow) Object.defineProperty(globalThis, 'window', previousWindow);
    else Reflect.deleteProperty(globalThis, 'window');
    if (previousDocument) Object.defineProperty(globalThis, 'document', previousDocument);
    else Reflect.deleteProperty(globalThis, 'document');
  }
});

// Automatic sys.page_view collection (opt-in autoPageViews).
const pageViewConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.page_view',
        schema_version: 1,
        schema: { type: 'object', additionalProperties: false, properties: {} },
      },
    ],
  };
};

function setupAuto(
  handler: (body: Body, init: RequestInit) => Response | Promise<Response>,
  extra: Partial<ClientOptions> = {},
  cfg: PublicConfig = pageViewConfig(),
) {
  const clock = new TestClock();
  let id = 100;
  const bodies: string[] = [];
  const fetcher: typeof fetch = async (input, init = {}) => {
    if (String(input).includes('/v1/config')) return json(cfg, 200, { ETag: etag });
    bodies.push(String(init.body));
    return handler(JSON.parse(String(init.body)) as Body, init);
  };
  const client = createEviaClient({
    endpoint: 'https://events.example',
    writeKey: 'public-example',
    projectId,
    environment: 'development',
    fetch: fetcher,
    clock,
    randomUUID: () => `00000000-0000-4000-8000-${String(++id).padStart(12, '0')}`,
    storage: null,
    lifecycle: false,
    requestTimeoutMs: 100,
    flushIntervalMs: 10,
    retryBaseMs: 10,
    ...extra,
  });
  return { client, clock, bodies };
}

test('automatic page views stay disabled unless autoPageViews is opted in', async () => {
  const { client, bodies } = setup((body) => receipt(body));
  client.navigate({ navigationType: 'push' });
  client.reset();
  await pump();
  await client.flush();
  assert.equal(bodies.length, 0);
  client.destroy();
});

test('auto page views emit sys.page_view for initial load, navigate and reset', async () => {
  const { client, bodies } = setupAuto((body) => receipt(body), { autoPageViews: true });
  const navPage = client.navigate({ navigationType: 'push' });
  client.reset();
  await pump();
  await client.flush();

  assert.equal(bodies.length, 1);
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.equal(sent.events.length, 3);
  for (const event of sent.events) {
    assert.equal(event.event_key, 'sys.page_view');
    assert.deepEqual(event.properties, {});
    assert.equal(event.schema_version, 1);
  }
  const pageViewIds = sent.events.map((event) => event.context.page_view_id);
  assert.equal(new Set(pageViewIds).size, 3, 'each page view must use a distinct page_view_id');
  assert.deepEqual(
    sent.events.map((event) => event.context.navigation_type),
    ['initial', 'push', 'initial'],
  );
  assert.ok(
    sent.events.some((event) => event.context.page_view_id === navPage.page_view_id),
    'the navigate page view must be reported with its new page_view_id',
  );
  client.destroy();
});

test('unpublished sys.page_view fails closed without breaking navigation', async () => {
  // Configuration deliberately omits sys.page_view.
  const { client, bodies } = setupAuto((body) => receipt(body), { autoPageViews: true }, config());
  const page = client.navigate({ navigationType: 'replace' });
  assert.equal(page.navigation_type, 'replace');
  await pump();
  await client.flush();
  assert.equal(client.pending, 0, 'rejected automatic events must leave the queue cleanly');
  assert.equal(bodies.length, 0, 'unpublished system events must never be transmitted');
  client.destroy();
});

test('a full queue drops automatic page views instead of breaking navigation', async () => {
  const { client } = setupAuto((body) => receipt(body), {
    autoPageViews: true,
    maxQueueEvents: 1,
  });
  // Construction already queued the initial automatic page view, so the cap is reached.
  assert.equal(client.pending, 1);
  const page = client.navigate({ navigationType: 'push' });
  assert.equal(client.pending, 1, 'the back-pressured automatic event must be dropped, not queued');
  assert.equal(page.page_view_id.length, 36);
  client.destroy();
});

test('public emit still cannot forge reserved system events', async () => {
  const { client } = setupAuto((body) => receipt(body), { autoPageViews: true });
  assert.throws(
    () => client.emit('sys.page_view'),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_CUSTOM_EVENT_KEY',
  );
  client.destroy();
});

// Automatic sys.identity_link collection (opt-in autoIdentityLink).
const identityLinkConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.identity_link',
        schema_version: 1,
        schema: { type: 'object', additionalProperties: false, properties: {} },
      },
    ],
  };
};

test('identity link stays disabled unless autoIdentityLink is opted in', async () => {
  const { client, bodies } = setup((body) => receipt(body));
  client.identify('known-user');
  await pump();
  await client.flush();
  assert.equal(bodies.length, 0);
  client.destroy();
});

test('identify enqueues sys.identity_link carrying both uid and anonymous id', async () => {
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { autoIdentityLink: true },
    identityLinkConfig(),
  );
  client.identify('known-user');
  await pump();
  await client.flush();

  assert.equal(bodies.length, 1);
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.equal(sent.events.length, 1);
  const event = sent.events[0]!;
  assert.equal(event.event_key, 'sys.identity_link');
  assert.deepEqual(event.properties, {}, 'identity link must collect no credentials or properties');
  assert.equal(event.schema_version, 1);
  assert.equal(event.identity.uid, 'known-user');
  assert.equal(typeof event.identity.anonymous_id, 'string');
  assert.equal(event.identity.anonymous_id!.length, 36);
  assert.notEqual(event.identity.anonymous_id, 'known-user');
  client.destroy();
});

test('unpublished identity link fails closed while identify still sets the uid', async () => {
  // Configuration deliberately omits sys.identity_link.
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { autoIdentityLink: true },
    config(),
  );
  client.identify('known-user');
  await pump();
  await client.flush();
  assert.equal(client.pending, 0, 'the rejected automatic event must leave the queue cleanly');
  assert.equal(bodies.length, 0, 'unpublished system events must never be transmitted');

  // The primary effect of identify must survive the failed automatic event.
  const follow = client.emit('page_opened', { value: 1 });
  await pump();
  await client.flush();
  assert.equal((await follow.result).status, 'accepted');
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.equal(sent.events[0]!.identity.uid, 'known-user');
  client.destroy();
});

test('a full queue drops the identity link without undoing identify or throwing', async () => {
  const { client } = setupAuto(
    (body) => receipt(body),
    { autoIdentityLink: true, maxQueueEvents: 1 },
    identityLinkConfig(),
  );
  // Fill the single slot with a custom event before identify.
  client.emit('page_opened', { value: 1 });
  assert.equal(client.pending, 1);
  client.identify('known-user');
  assert.equal(client.pending, 1, 'the back-pressured identity link must be dropped, not queued');
  client.destroy();
});

test('reset does not emit an identity link even when autoIdentityLink is enabled', async () => {
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { autoIdentityLink: true },
    identityLinkConfig(),
  );
  client.reset();
  await pump();
  await client.flush();
  assert.equal(bodies.length, 0, 'reset rotates identity but must not record an identity link');
  client.destroy();
});

test('public emit still cannot forge sys.identity_link', async () => {
  const { client } = setupAuto(
    (body) => receipt(body),
    { autoIdentityLink: true },
    identityLinkConfig(),
  );
  assert.throws(
    () => client.emit('sys.identity_link'),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_CUSTOM_EVENT_KEY',
  );
  client.destroy();
});

// Reserved sys.operation_failed reporting via reportOperationFailure().
const operationFailedConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.operation_failed',
        schema_version: 1,
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: {
            operation: { type: 'string' },
            error_category: { type: 'string' },
            error_code: { type: 'string' },
            retryable: { type: 'boolean' },
          },
        },
      },
    ],
  };
};

function setupOperation(
  handler: (body: Body, init: RequestInit) => Response | Promise<Response>,
  extra: Partial<ClientOptions> = {},
  cfg: PublicConfig = operationFailedConfig(),
) {
  const clock = new TestClock();
  let id = 100;
  const bodies: string[] = [];
  const fetcher: typeof fetch = async (input, init = {}) => {
    if (String(input).includes('/v1/config')) return json(cfg, 200, { ETag: etag });
    bodies.push(String(init.body));
    return handler(JSON.parse(String(init.body)) as Body, init);
  };
  const client = createEviaClient({
    endpoint: 'https://events.example',
    writeKey: 'public-example',
    projectId,
    environment: 'development',
    fetch: fetcher,
    clock,
    randomUUID: () => `00000000-0000-4000-8000-${String(++id).padStart(12, '0')}`,
    storage: null,
    lifecycle: false,
    requestTimeoutMs: 100,
    flushIntervalMs: 10,
    retryBaseMs: 10,
    ...extra,
  });
  return { client, bodies };
}

test('reportOperationFailure sends a sanitized sys.operation_failed with snake_case properties', async () => {
  const { client, bodies } = setupOperation((body) => receipt(body));
  const emission = client.reportOperationFailure({
    operation: 'post_like',
    errorCategory: 'business',
    retryable: false,
    errorCode: 'REJECTED',
  });
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.equal(sent.events.length, 1);
  const event = sent.events[0]!;
  assert.equal(event.event_key, 'sys.operation_failed');
  assert.equal(event.schema_version, 1);
  assert.deepEqual(event.properties, {
    operation: 'post_like',
    error_category: 'business',
    retryable: false,
    error_code: 'REJECTED',
  });
  client.destroy();
});

test('error code is optional and absent when omitted', async () => {
  const { client, bodies } = setupOperation((body) => receipt(body));
  client.reportOperationFailure({
    operation: 'load_feed',
    errorCategory: 'timeout',
    retryable: true,
  });
  await pump();
  await client.flush();
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.deepEqual(sent.events[0]!.properties, {
    operation: 'load_feed',
    error_category: 'timeout',
    retryable: true,
  });
  assert.equal('error_code' in sent.events[0]!.properties, false);
  client.destroy();
});

test('every closed error category is accepted', () => {
  const { client } = setupOperation((body) => receipt(body));
  for (const errorCategory of ['validation', 'network', 'timeout', 'http', 'business'] as const) {
    assert.doesNotThrow(() =>
      client.reportOperationFailure({ operation: 'op', errorCategory, retryable: false }),
    );
  }
  client.destroy();
});

test('reportOperationFailure rejects unknown categories, non-boolean retryable and bad operation', () => {
  const { client } = setupOperation((body) => receipt(body));
  assert.throws(
    () =>
      client.reportOperationFailure({
        operation: 'op',
        errorCategory: 'kernel' as never,
        retryable: false,
      }),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_ERROR_CATEGORY',
  );
  assert.throws(
    () =>
      client.reportOperationFailure({
        operation: 'op',
        errorCategory: 'http',
        retryable: 'yes' as never,
      }),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_RETRYABLE',
  );
  assert.throws(
    () => client.reportOperationFailure({ operation: '', errorCategory: 'http', retryable: false }),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_TEXT',
  );
  client.destroy();
});

test('reportOperationFailure rebuilds payload and drops unknown caller fields', async () => {
  const { client, bodies } = setupOperation((body) => receipt(body));
  // Extra fields on the caller object must not leak into the bounded event.
  const dirty = {
    operation: 'post_like',
    errorCategory: 'network' as const,
    retryable: true,
    secret: 'must-not-leak',
    rawError: new Error('boom'),
  };
  client.reportOperationFailure(dirty);
  await pump();
  await client.flush();
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.deepEqual(Object.keys(sent.events[0]!.properties).sort(), [
    'error_category',
    'operation',
    'retryable',
  ]);
  assert.equal('secret' in sent.events[0]!.properties, false);
  client.destroy();
});

test('unpublished operation failure fails closed via the publication gate', async () => {
  // Configuration deliberately omits sys.operation_failed.
  const { client, bodies } = setupOperation((body) => receipt(body), {}, config());
  const emission = client.reportOperationFailure({
    operation: 'post_like',
    errorCategory: 'business',
    retryable: false,
  });
  await pump();
  await client.flush();
  assert.equal((await emission.result).code, 'EVENT_NOT_PUBLISHED');
  assert.equal(bodies.length, 0, 'unpublished reserved events must never be transmitted');
  client.destroy();
});

test('public emit still cannot forge sys.operation_failed', () => {
  const { client } = setupOperation((body) => receipt(body));
  assert.throws(
    () => client.emit('sys.operation_failed'),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_CUSTOM_EVENT_KEY',
  );
  client.destroy();
});
