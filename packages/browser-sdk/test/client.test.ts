import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createEviaClient, EviaSDKError } from '../src/index.js';
import type {
  ClientOptions,
  Clock,
  EncryptedContentChunk,
  HeatmapClick,
  HeatmapHost,
  PublicConfig,
  ScrollHost,
  StoragePort,
} from '../src/index.js';

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

// Reserved sys.session_end collection (opt-in autoSessionEnd).
const sessionEndConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.session_end',
        schema_version: 1,
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: { reason: { type: 'string' } },
        },
      },
    ],
  };
};

function setupSession(
  handler: (body: Body, init: RequestInit) => Response | Promise<Response>,
  extra: Partial<ClientOptions> = {},
  cfg: PublicConfig = sessionEndConfig(),
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
    sessionTimeoutMs: 60000,
    ...extra,
  });
  return { client, clock, bodies };
}

test('session end stays disabled unless autoSessionEnd is opted in', async () => {
  const { client, clock, bodies } = setup((body) => receipt(body));
  client.identify('user-a');
  client.identify('user-b');
  client.reset();
  client.emit('page_opened', { value: 1 });
  await clock.advance(60001);
  client.emit('page_opened', { value: 2 });
  await pump();
  await client.flush();
  const all = bodies.flatMap((raw) => (JSON.parse(raw) as Body).events.map((e) => e.event_key));
  assert.ok(!all.includes('sys.session_end'), `no session end expected, got ${all.join(',')}`);
  client.destroy();
});

test('reset emits a logout session end carrying the ending identity', async () => {
  const { client, bodies } = setupSession((body) => receipt(body), { autoSessionEnd: true });
  client.identify('user-a');
  client.reset();
  await pump();
  await client.flush();
  const sent = JSON.parse(bodies[0]!) as Body;
  const ends = sent.events.filter((e) => e.event_key === 'sys.session_end');
  assert.equal(ends.length, 1);
  assert.deepEqual(ends[0]!.properties, { reason: 'logout' });
  assert.equal(ends[0]!.identity.uid, 'user-a', 'logout event must keep the cleared uid context');
  client.destroy();
});

test('changing the uid emits one account_switch session end for the old account', async () => {
  const { client, bodies } = setupSession((body) => receipt(body), { autoSessionEnd: true });
  client.identify('user-a');
  client.identify('user-a'); // same uid is not a switch
  client.identify('user-b'); // first real switch
  await pump();
  await client.flush();
  const events = bodies.flatMap((raw) => (JSON.parse(raw) as Body).events);
  const ends = events.filter((e) => e.event_key === 'sys.session_end');
  assert.equal(ends.length, 1);
  assert.deepEqual(ends[0]!.properties, { reason: 'account_switch' });
  assert.equal(ends[0]!.identity.uid, 'user-a', 'switch event must keep the prior uid context');
  client.destroy();
});

test('inactivity rotates the session and emits an idle end bound to the old session', async () => {
  const { client, clock, bodies } = setupSession((body) => receipt(body), { autoSessionEnd: true });
  const first = client.emit('page_opened', { value: 1 });
  await pump();
  await client.flush();
  assert.equal((await first.result).status, 'accepted');
  const firstSession = (JSON.parse(bodies[0]!) as Body).events[0]!.context.session_id;

  await clock.advance(60001);
  const second = client.emit('page_opened', { value: 2 });
  await pump();
  await client.flush();
  assert.equal((await second.result).status, 'accepted');

  const events = bodies.flatMap((raw) => (JSON.parse(raw) as Body).events);
  const idle = events.find((e) => e.event_key === 'sys.session_end');
  assert.ok(idle, 'an idle session end must be emitted on timeout rotation');
  assert.deepEqual(idle!.properties, { reason: 'idle' });
  assert.equal(
    idle!.context.session_id,
    firstSession,
    'idle event must describe the ending session',
  );
  const nextCustom = events.filter((e) => e.event_key === 'page_opened').at(-1)!;
  assert.notEqual(
    nextCustom.context.session_id,
    firstSession,
    'the new event must use a fresh session',
  );
  client.destroy();
});

test('unpublished session end fails closed without breaking the underlying action', async () => {
  // Configuration deliberately omits sys.session_end.
  const { client, bodies } = setupSession(
    (body) => receipt(body),
    { autoSessionEnd: true },
    config(),
  );
  client.reset();
  await pump();
  await client.flush();
  assert.equal(client.pending, 0, 'the rejected automatic event must leave the queue cleanly');
  assert.equal(bodies.length, 0, 'unpublished reserved events must never be transmitted');
  client.destroy();
});

test('a full queue drops the session end without breaking reset or identify', async () => {
  const { client } = setupSession((body) => receipt(body), {
    autoSessionEnd: true,
    maxQueueEvents: 1,
  });
  client.emit('page_opened', { value: 1 });
  assert.equal(client.pending, 1);
  client.reset();
  client.identify('user-b');
  assert.equal(client.pending, 1, 'back-pressured session ends must be dropped, not queued');
  client.destroy();
});

test('public emit still cannot forge sys.session_end', () => {
  const { client } = setupSession((body) => receipt(body), { autoSessionEnd: true });
  assert.throws(
    () => client.emit('sys.session_end'),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_CUSTOM_EVENT_KEY',
  );
  client.destroy();
});

// Reserved sys.page_scroll collection (opt-in autoPageScrolls) with an injectable host.
class FakeScrollHost implements ScrollHost {
  private listener: (() => void) | null = null;
  private current = 0;
  subscribed = false;
  throwOnRatio = false;
  subscribe(listener: () => void): () => void {
    this.listener = listener;
    this.subscribed = true;
    return () => {
      this.listener = null;
      this.subscribed = false;
    };
  }
  ratio(): number {
    if (this.throwOnRatio) throw new Error('synthetic host failure');
    return this.current;
  }
  setRatio(value: number): void {
    this.current = value;
    this.listener?.();
  }
}

const pageScrollConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.page_scroll',
        schema_version: 1,
        schema: { type: 'object', additionalProperties: false },
      },
    ],
  };
};

function setupScroll(
  handler: (body: Body, init: RequestInit) => Response | Promise<Response>,
  extra: Partial<ClientOptions> = {},
  cfg: PublicConfig = pageScrollConfig(),
) {
  const clock = new TestClock();
  let id = 100;
  const bodies: string[] = [];
  const scroll = new FakeScrollHost();
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
    scrollHost: scroll,
    scrollIdleMs: 1000,
    ...extra,
  });
  return { client, clock, bodies, scroll };
}

const scrollEvents = (bodies: string[]) =>
  bodies
    .flatMap((raw) => (JSON.parse(raw) as Body).events)
    .filter((e) => e.event_key === 'sys.page_scroll');

test('page scroll collection stays disabled unless autoPageScrolls is opted in', async () => {
  const { client, clock, scroll, bodies } = setupScroll((body) => receipt(body));
  scroll.setRatio(0.5);
  await clock.advance(1100);
  await client.flush();
  assert.equal(scrollEvents(bodies).length, 0);
  client.destroy();
});

test('scrolling stops coalesces one bounded down segment', async () => {
  const { client, clock, scroll, bodies } = setupScroll((body) => receipt(body), {
    autoPageScrolls: true,
  });
  scroll.setRatio(0);
  scroll.setRatio(0.3);
  scroll.setRatio(0.6);
  await clock.advance(1100);
  await client.flush();

  const events = scrollEvents(bodies);
  assert.equal(events.length, 1);
  assert.deepEqual(
    { ...events[0]!.properties, duration_ms: undefined },
    { from_ratio: 0, to_ratio: 0.6, max_ratio: 0.6, direction: 'down', duration_ms: undefined },
  );
  const duration = events[0]!.properties.duration_ms as number;
  assert.ok(Number.isSafeInteger(duration) && duration >= 1000, `unexpected duration ${duration}`);
  client.destroy();
});

test('reverse movement yields mixed and up directions with correct bounds', async () => {
  const {
    client: mixedClient,
    clock: mixedClock,
    scroll: mixedHost,
    bodies: mixedBodies,
  } = setupScroll((body) => receipt(body), { autoPageScrolls: true });
  mixedHost.setRatio(0);
  mixedHost.setRatio(0.5);
  mixedHost.setRatio(0.3);
  await mixedClock.advance(1100);
  await mixedClient.flush();
  const mixed = scrollEvents(mixedBodies)[0]!.properties;
  assert.equal(mixed.direction, 'mixed');
  assert.equal(mixed.from_ratio, 0);
  assert.equal(mixed.to_ratio, 0.3);
  assert.equal(mixed.max_ratio, 0.5);
  mixedClient.destroy();

  const { client, clock, scroll, bodies } = setupScroll((body) => receipt(body), {
    autoPageScrolls: true,
  });
  scroll.setRatio(0.6);
  scroll.setRatio(0.3);
  await clock.advance(1100);
  await client.flush();
  assert.equal(scrollEvents(bodies)[0]!.properties.direction, 'up');
  client.destroy();
});

test('ratios outside [0,1] are clamped', async () => {
  const { client, clock, scroll, bodies } = setupScroll((body) => receipt(body), {
    autoPageScrolls: true,
  });
  scroll.setRatio(0);
  scroll.setRatio(2);
  await clock.advance(1100);
  await client.flush();
  const props = scrollEvents(bodies)[0]!.properties;
  assert.equal(props.to_ratio, 1);
  assert.equal(props.max_ratio, 1);
  client.destroy();
});

test('scroll production is capped at twelve segments per minute', async () => {
  const { client, clock, scroll, bodies } = setupScroll((body) => receipt(body), {
    autoPageScrolls: true,
    maxQueueEvents: 200,
  });
  for (let i = 0; i < 13; i++) {
    scroll.setRatio(0);
    scroll.setRatio(0.5);
    await clock.advance(1100); // finalize this segment (~1.1s apart, still inside one minute)
  }
  await client.flush();
  assert.equal(
    scrollEvents(bodies).length,
    12,
    'only twelve scroll segments may be produced per minute',
  );
  client.destroy();
});

test('navigation discards an in-flight segment that has not stopped', async () => {
  const { client, clock, scroll, bodies } = setupScroll((body) => receipt(body), {
    autoPageScrolls: true,
  });
  scroll.setRatio(0);
  scroll.setRatio(0.4);
  client.navigate({ navigationType: 'push' });
  await clock.advance(1100);
  await client.flush();
  assert.equal(scrollEvents(bodies).length, 0);
  client.destroy();
});

test('unpublished page scroll fails closed without throwing', async () => {
  const { client, clock, scroll, bodies } = setupScroll(
    (body) => receipt(body),
    { autoPageScrolls: true },
    config(),
  );
  scroll.setRatio(0);
  scroll.setRatio(0.5);
  await clock.advance(1100);
  await client.flush();
  assert.equal(bodies.length, 0, 'unpublished reserved events must never be transmitted');
  assert.equal(client.pending, 0);
  client.destroy();
});

test('public emit still cannot forge sys.page_scroll', () => {
  const { client } = setupScroll((body) => receipt(body), { autoPageScrolls: true });
  assert.throws(
    () => client.emit('sys.page_scroll'),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_CUSTOM_EVENT_KEY',
  );
  client.destroy();
});

test('destroy unsubscribes the scroll host and ignores later activity', async () => {
  const { client, scroll } = setupScroll((body) => receipt(body), { autoPageScrolls: true });
  assert.equal(scroll.subscribed, true);
  client.destroy();
  assert.equal(scroll.subscribed, false, 'destroy must unsubscribe the scroll host to avoid leaks');
  // Activity after destroy must neither throw nor enqueue.
  assert.doesNotThrow(() => scroll.setRatio(0.5));
});

test('a throwing scroll host does not break the page listener or the client', async () => {
  const { client, scroll, bodies } = setupScroll((body) => receipt(body), {
    autoPageScrolls: true,
  });
  scroll.throwOnRatio = true;
  assert.doesNotThrow(() => scroll.setRatio(0.4));
  // The client stays usable for explicit events after the host failure.
  scroll.throwOnRatio = false;
  const emission = client.emit('page_opened', { value: 1 });
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  assert.equal(bodies.length, 1, 'only the explicit event should be sent');
  client.destroy();
});

test('public config accepts 207 events including the seven reserved sys keys', async () => {
  const sysKeys = [
    'sys.page_view',
    'sys.page_scroll',
    'sys.content_chunk',
    'sys.heatmap_click',
    'sys.identity_link',
    'sys.session_end',
    'sys.operation_failed',
  ];
  const buildConfig = (count: number): PublicConfig => ({
    ...config(),
    events: [
      ...Array.from({ length: count - sysKeys.length }, (_, index) => ({
        event_key: `event_${index}`,
        schema_version: 1,
        schema: { type: 'object' },
      })),
      ...sysKeys.map((event_key) => ({
        event_key,
        schema_version: 1,
        schema: { type: 'object', additionalProperties: false, properties: {} },
      })),
    ],
  });
  const within = setup(
    () => {
      throw new Error('unused');
    },
    { fetch: async () => json(buildConfig(207)) },
  );
  await within.client.init();
  within.client.destroy();

  const over = setup(
    () => {
      throw new Error('unused');
    },
    { fetch: async () => json(buildConfig(208)) },
  );
  await assert.rejects(over.client.init(), /INVALID_CONFIG/);
  over.client.destroy();
});

test('public config validates browser-safe content encryption keys', async () => {
  const valid = setup(
    () => {
      throw new Error('unused');
    },
    {
      fetch: async () =>
        json({
          ...config(),
          content_encryption: {
            key_id: 'content-2026-11',
            alg: 'RSA-OAEP-256',
            public_jwk: { kty: 'RSA', n: 'public-modulus', e: 'AQAB' },
          },
        }),
    },
  );
  const loaded = await valid.client.init();
  assert.equal(loaded.content_encryption?.key_id, 'content-2026-11');
  valid.client.destroy();

  for (const content_encryption of [
    { key_id: 'bad key', alg: 'RSA-OAEP-256', public_jwk: { kty: 'RSA', n: 'n', e: 'AQAB' } },
    {
      key_id: 'key',
      alg: 'RSA-OAEP-256',
      public_jwk: { kty: 'RSA', n: 'n', e: 'AQAB', d: 'private' },
    },
    { key_id: 'key', alg: 'RSA1_5', public_jwk: { kty: 'RSA', n: 'n', e: 'AQAB' } },
  ]) {
    const invalid = setup(
      () => {
        throw new Error('unused');
      },
      { fetch: async () => json({ ...config(), content_encryption }) },
    );
    await assert.rejects(invalid.client.init(), /INVALID_CONFIG/);
    invalid.client.destroy();
  }
});

test('reserved sys events in public config are transmitted with the project release revision', async () => {
  const cfg: PublicConfig = {
    ...config(4),
    events: [
      ...config(4).events,
      {
        event_key: 'sys.operation_failed',
        schema_version: 1,
        schema: { type: 'object', additionalProperties: false },
      },
    ],
  };
  const { client, bodies } = setupOperation((body) => receipt(body), {}, cfg);
  const emission = client.reportOperationFailure({
    operation: 'checkout_submit',
    errorCategory: 'timeout',
    retryable: true,
  });
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  assert.equal(bodies.length, 1);
  const sent = JSON.parse(bodies[0]!) as Body;
  assert.equal(sent.events.length, 1);
  const event = sent.events[0]!;
  assert.equal(event.event_key, 'sys.operation_failed');
  // The envelope revision is the source's active project release revision, kept
  // as provenance even though the collector resolves sys.* via the system catalog.
  assert.equal(event.release_revision, 4);
  assert.equal(event.schema_version, 1);
  client.destroy();
});

class MapStorage implements StoragePort {
  private map = new Map<string, string>();
  getItem = (key: string): string | null => this.map.get(key) ?? null;
  setItem = (key: string, value: string): void => {
    this.map.set(key, value);
  };
  keys = (): string[] => [...this.map.keys()];
  read = (key: string): string | null => this.map.get(key) ?? null;
}
const queueStorageKey = `evia:${projectId}:development:public-example:queue`;
function durableDraft(eventId: string) {
  const base = Date.parse('2026-11-09T12:00:00Z');
  return {
    event_id: eventId,
    event_key: 'page_opened',
    occurred_at: new Date(base).toISOString(),
    identity: { anonymous_id: '00000000-0000-4000-8000-0000000000bb' },
    context: {
      session_id: 's',
      tab_id: 't',
      sequence: 1,
      page_view_id: 'p',
      navigation_id: 'n',
      navigation_type: 'initial' as const,
    },
    sdk: { name: 'evia-browser' as const, version: '0.1.0' },
    properties: {},
  };
}

test('opt-in persistent queue survives reload and redelivers the stable event id', async () => {
  const storage = new MapStorage();
  const delivered: Body[] = [];
  const handler = (body: Body) => {
    delivered.push(body);
    return receipt(body);
  };
  // First page load: the event is durably mirrored but never flushed.
  const first = setup(handler, { storage, persistQueue: true });
  await first.client.init();
  const emission = first.client.emit('page_opened');
  await pump();
  assert.equal(first.client.pending, 1);
  const snapshot = JSON.parse(storage.read(queueStorageKey)!);
  assert.equal(snapshot.format, 'evia.queue.v1');
  assert.equal(snapshot.entries[0].eventId, emission.eventId);
  // Simulate abrupt unload: neither destroy() nor flush() runs, yet the snapshot survives.

  // A second page load over the same storage rehydrates and delivers the same event.
  const second = setup(handler, { storage, persistQueue: true });
  assert.equal(second.client.pending, 1);
  await second.client.init();
  await pump();
  await second.client.flush();
  assert.equal(delivered.length, 1);
  assert.equal(delivered[0]!.events.length, 1);
  assert.equal(delivered[0]!.events[0]!.event_id, emission.eventId);
  const emptied = JSON.parse(storage.read(queueStorageKey)!);
  assert.equal(emptied.entries.length, 0);
  second.client.destroy();
});

test('queue persistence is opt-in and writes no queue key by default', async () => {
  const storage = new MapStorage();
  const { client } = setup((body) => receipt(body), { storage });
  await client.init();
  const emission = client.emit('page_opened');
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  assert.equal(
    storage.keys().some((key) => key.endsWith(':queue')),
    false,
  );
  client.destroy();
});

test('rehydration prunes expired and malformed snapshot entries', async () => {
  const storage = new MapStorage();
  const base = Date.parse('2026-11-09T12:00:00Z');
  const draft = durableDraft('00000000-0000-4000-8000-0000000000aa');
  const valid = {
    eventId: draft.event_id,
    draft,
    createdAt: base,
    bytes: 1,
    attempts: 0,
    ambiguous: false,
    json: JSON.stringify({ ...draft, schema_version: 1, release_revision: 1 }),
  };
  const expired = {
    ...valid,
    eventId: '00000000-0000-4000-8000-0000000000cc',
    createdAt: base - 400_000,
  };
  const malformed = { eventId: 'not-a-uuid' };
  storage.setItem(
    queueStorageKey,
    JSON.stringify({ format: 'evia.queue.v1', entries: [expired, malformed, valid] }),
  );
  const delivered: Body[] = [];
  const { client } = setup(
    (body) => {
      delivered.push(body);
      return receipt(body);
    },
    { storage, persistQueue: true },
  );
  assert.equal(client.pending, 1);
  await client.init();
  await pump();
  await client.flush();
  assert.equal(delivered.length, 1);
  assert.equal(delivered[0]!.events[0]!.event_id, valid.eventId);
  const pruned = JSON.parse(storage.read(queueStorageKey)!);
  assert.equal(pruned.entries.length, 0);
  client.destroy();
});

test('rehydrated entries awaiting configuration are resolved before delivery', async () => {
  const storage = new MapStorage();
  const base = Date.parse('2026-11-09T12:00:00Z');
  const draft = durableDraft('00000000-0000-4000-8000-0000000000ad');
  storage.setItem(
    queueStorageKey,
    JSON.stringify({
      format: 'evia.queue.v1',
      entries: [
        {
          eventId: draft.event_id,
          draft,
          createdAt: base,
          bytes: 1,
          attempts: 0,
          ambiguous: false,
        },
      ],
    }),
  );
  const delivered: Body[] = [];
  const { client } = setup(
    (body) => {
      delivered.push(body);
      return receipt(body);
    },
    { storage, persistQueue: true },
  );
  assert.equal(client.pending, 1);
  await client.init();
  await pump();
  await client.flush();
  assert.equal(delivered.length, 1);
  const event = delivered[0]!.events[0]!;
  assert.equal(event.event_id, draft.event_id);
  assert.equal(event.schema_version, 1);
  assert.equal(event.release_revision, 1);
  client.destroy();
});

test('a corrupt snapshot fails soft to an empty in-memory queue', async () => {
  const storage = new MapStorage();
  storage.setItem(queueStorageKey, 'not-json{');
  const { client } = setup((body) => receipt(body), { storage, persistQueue: true });
  assert.equal(client.pending, 0);
  await client.init();
  const emission = client.emit('page_opened');
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  client.destroy();
});

test('a failing storage port fails soft and keeps in-memory delivery working', async () => {
  const throwing: StoragePort = {
    getItem: () => {
      throw new Error('denied');
    },
    setItem: () => {
      throw new Error('denied');
    },
  };
  const { client } = setup((body) => receipt(body), {
    storage: throwing,
    persistQueue: true,
  });
  assert.equal(client.pending, 0);
  await client.init();
  const emission = client.emit('page_opened');
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  client.destroy();
});

// --- cross-document session continuity (persistSession) ---

const sessionStorageKey = `evia:${projectId}:development:public-example:session`;
const readSession = (port: StoragePort): Record<string, unknown> | null => {
  const raw = port.getItem(sessionStorageKey);
  return raw ? (JSON.parse(raw) as Record<string, unknown>) : null;
};

test('persistSession continues the session scope across a simulated reload', async () => {
  const port = new MapStorage();
  const delivered: Body[] = [];
  const handler = (body: Body) => {
    delivered.push(body);
    return receipt(body);
  };
  const first = setup(handler, { sessionStorage: port, persistSession: true });
  await first.client.init();
  first.client.emit('page_opened');
  first.client.emit('item_opened');
  await pump();
  await first.client.flush();
  const scopeA = first.client.emit('page_opened');
  await pump();
  await first.client.flush();
  assert.equal((await scopeA.result).status, 'accepted');
  const eventA = delivered[0]!.events[0] as any;
  first.client.destroy();

  // Same tab, new document: the scope is restored verbatim.
  const second = setup(handler, { sessionStorage: port, persistSession: true });
  const emission = second.client.emit('page_opened');
  await pump();
  await second.client.flush();
  assert.equal((await emission.result).status, 'accepted');
  const continued = delivered[2]!.events[0] as any;
  assert.equal(continued.context.session_id, eventA.context.session_id);
  assert.equal(continued.context.tab_id, eventA.context.tab_id);
  // The sequence is strictly monotonic across the document boundary.
  assert.equal(continued.context.sequence, eventA.context.sequence + 3);
  const snapshot = readSession(port)!;
  assert.equal(snapshot.format, 'evia.session.v1');
  assert.equal(snapshot.session_id, eventA.context.session_id);
  assert.equal(snapshot.tab_id, eventA.context.tab_id);
  second.client.destroy();
});

test('session persistence is opt-in and writes no session key by default', async () => {
  const port = new MapStorage();
  const { client } = setup((body) => receipt(body), { sessionStorage: port });
  await client.init();
  client.emit('page_opened');
  await pump();
  await client.flush();
  assert.equal(
    port.keys().some((key) => key.endsWith(':session')),
    false,
  );
  client.destroy();
});

test('a restored idle session rotates lazily exactly like the live path', async () => {
  const port = new MapStorage();
  const delivered: Body[] = [];
  const handler = (body: Body) => {
    delivered.push(body);
    return receipt(body);
  };
  const first = setupSession(handler, {
    sessionStorage: port,
    persistSession: true,
    autoSessionEnd: true,
  });
  const before = first.client.emit('page_opened');
  await pump();
  await first.client.flush();
  assert.equal((await before.result).status, 'accepted');
  const expiredSession = (delivered[0]!.events[0] as any).context.session_id;
  first.client.destroy();

  // Rehydrate long after the timeout: construction restores verbatim and does
  // not emit; the first activity observes the idle end with the old session.
  const second = setupSession(handler, {
    sessionStorage: port,
    persistSession: true,
    autoSessionEnd: true,
  });
  await second.clock.advance(24 * 60 * 60 * 1000);
  await second.client.init();
  const after = second.client.emit('page_opened');
  await pump();
  await second.client.flush();
  assert.equal((await after.result).status, 'accepted');
  const endEvent = delivered[1]!.events.find(
    (event) => (event as any).event_key === 'sys.session_end',
  ) as any;
  const freshEvent = delivered[1]!.events.find(
    (event) => (event as any).event_key === 'page_opened',
  ) as any;
  assert.equal(endEvent.context.session_id, expiredSession);
  assert.equal(endEvent.properties.reason, 'idle');
  assert.notEqual(freshEvent.context.session_id, expiredSession);
  assert.equal(freshEvent.context.tab_id, endEvent.context.tab_id);
  second.client.destroy();
});

test('reset discards the persisted session scope for the next document', async () => {
  const port = new MapStorage();
  const first = setup(() => receipt({ batch_id: 'b', events: [] }), {
    sessionStorage: port,
    persistSession: true,
  });
  await first.client.init();
  const scopeBefore = readSession(port)!;
  first.client.reset();
  const scopeAfter = readSession(port)!;
  assert.notEqual(scopeAfter.session_id, scopeBefore.session_id);
  assert.equal(scopeAfter.tab_id, scopeBefore.tab_id);
  first.client.destroy();

  const second = setup((body) => receipt(body), {
    sessionStorage: port,
    persistSession: true,
  });
  const snapshot = readSession(port)!;
  assert.equal(snapshot.session_id, scopeAfter.session_id);
  second.client.destroy();
});

test('two tabs stay isolated because their session ports are separate', async () => {
  const tabA = new MapStorage();
  const tabB = new MapStorage();
  const a = setup((body) => receipt(body), { sessionStorage: tabA, persistSession: true });
  // Distinct uuid stream: isolation must come from the ports, not from a
  // shared deterministic generator.
  let id = 500;
  const b = setup((body) => receipt(body), {
    sessionStorage: tabB,
    persistSession: true,
    randomUUID: () => `00000000-0000-4000-8000-${String(++id).padStart(12, '0')}`,
  });
  await a.client.init();
  await b.client.init();
  a.client.emit('page_opened');
  b.client.emit('page_opened');
  await pump();
  const scopeA = readSession(tabA)!;
  const scopeB = readSession(tabB)!;
  assert.notEqual(scopeA.session_id, scopeB.session_id);
  assert.notEqual(scopeA.tab_id, scopeB.tab_id);
  a.client.destroy();
  b.client.destroy();
});

test('corrupt or foreign session snapshots fail soft to a fresh scope', async () => {
  for (const raw of [
    'not json',
    JSON.stringify({ format: 'evia.queue.v1', session_id: 'x' }),
    JSON.stringify({
      format: 'evia.session.v1',
      session_id: 'not-a-uuid',
      tab_id: '00000000-0000-4000-8000-0000000000aa',
      sequence: 3,
      last_activity: 1,
    }),
    JSON.stringify({
      format: 'evia.session.v1',
      session_id: '00000000-0000-4000-8000-0000000000aa',
      tab_id: '00000000-0000-4000-8000-0000000000bb',
      sequence: -1,
      last_activity: 1,
    }),
  ]) {
    const port = new MapStorage();
    port.setItem(sessionStorageKey, raw);
    const { client } = setup((body) => receipt(body), {
      sessionStorage: port,
      persistSession: true,
    });
    const snapshot = readSession(port)!;
    assert.equal(snapshot.format, 'evia.session.v1');
    assert.match(String(snapshot.session_id), /^[0-9a-f-]{36}$/);
    assert.equal(snapshot.sequence, 0);
    assert.notEqual(snapshot.session_id, 'not-a-uuid');
    client.destroy();
  }
});

test('persistSession without a usable session port fails closed at construction', () => {
  assert.throws(
    () =>
      setup(() => receipt({ batch_id: 'b', events: [] }), {
        sessionStorage: null,
        persistSession: true,
      }),
    (error: unknown) =>
      error instanceof EviaSDKError && error.code === 'SESSION_STORAGE_UNAVAILABLE',
  );
});

test('session storage failures degrade softly and never break collection', async () => {
  const throwing: StoragePort = {
    getItem: () => {
      throw new Error('denied');
    },
    setItem: () => {
      throw new Error('denied');
    },
  };
  const { client } = setup((body) => receipt(body), {
    sessionStorage: throwing,
    persistSession: true,
  });
  await client.init();
  const emission = client.emit('page_opened');
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  client.destroy();
});

test('rehydrated queue events keep their original session while new events continue it', async () => {
  const port = new MapStorage();
  const store = new MapStorage();
  const delivered: Body[] = [];
  const handler = (body: Body) => {
    delivered.push(body);
    return receipt(body);
  };
  const first = setup(handler, {
    storage: store,
    sessionStorage: port,
    persistQueue: true,
    persistSession: true,
  });
  await first.client.init();
  const durable = first.client.emit('page_opened');
  await pump();
  const durableContext = (
    JSON.parse(store.getItem(queueStorageKey)!) as {
      entries: { draft: { context: { session_id: string; sequence: number } } }[];
    }
  ).entries[0]!.draft.context;
  assert.equal(durableContext.sequence, 1);
  // Abrupt unload: destroy() would drain the durable queue, so the first
  // client is simply abandoned exactly like a real reload does.

  const second = setup(handler, {
    storage: store,
    sessionStorage: port,
    persistQueue: true,
    persistSession: true,
  });
  assert.equal(second.client.pending, 1);
  await second.client.init();
  const fresh = second.client.emit('page_opened');
  await pump();
  await second.client.flush();
  // The first client was destroyed before delivery, so its own outcome promise
  // settles as dropped; the durable outcome is the collector receipt below.
  assert.equal((await fresh.result).status, 'accepted');
  const rehydrated = delivered[0]!.events.find(
    (event) => (event as any).event_id === durable.eventId,
  ) as any;
  const continued = delivered[0]!.events.find(
    (event) => (event as any).event_id === fresh.eventId,
  ) as any;
  assert.ok(rehydrated, 'the rehydrated event must be delivered');
  // The rehydrated snapshot is immutable history; the new document continues
  // the same session with a strictly greater sequence.
  assert.equal(rehydrated.context.session_id, durableContext.session_id);
  assert.equal(continued.context.session_id, durableContext.session_id);
  assert.equal(continued.context.sequence, 2);
  assert.equal(continued.context.tab_id, rehydrated.context.tab_id);
  second.client.destroy();
});

test('a persisted exhausted sequence keeps failing closed after restore', async () => {
  const port = new MapStorage();
  port.setItem(
    sessionStorageKey,
    JSON.stringify({
      format: 'evia.session.v1',
      session_id: '00000000-0000-4000-8000-0000000000aa',
      tab_id: '00000000-0000-4000-8000-0000000000bb',
      sequence: Number.MAX_SAFE_INTEGER,
      last_activity: Date.parse('2026-11-09T12:00:00Z'),
    }),
  );
  const { client } = setup((body) => receipt(body), {
    sessionStorage: port,
    persistSession: true,
  });
  await client.init();
  assert.throws(() => client.emit('page_opened'), /SEQUENCE_EXHAUSTED/);
  client.destroy();
});

// Automatic sys.heatmap_click collection (opt-in autoHeatmapClicks).
class FakeHeatmapHost implements HeatmapHost {
  private listeners = new Set<(sample: HeatmapClick) => void>();
  subscribe = (listener: (sample: HeatmapClick) => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };
  emit(sample: HeatmapClick): void {
    for (const listener of this.listeners) listener(sample);
  }
  get size(): number {
    return this.listeners.size;
  }
}

const heatmapConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.heatmap_click',
        schema_version: 1,
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: {
            x_ratio: { type: 'number' },
            y_ratio: { type: 'number' },
            viewport_width: { type: 'integer' },
            viewport_height: { type: 'integer' },
            element_path: { type: 'string' },
            element_index: { type: 'integer' },
          },
        },
      },
    ],
  };
};

const heatmapSample = (overrides: Partial<HeatmapClick> = {}): HeatmapClick => ({
  xRatio: 0.512345,
  yRatio: 0.333333,
  viewportWidth: 1366,
  viewportHeight: 900,
  elementPath: 'body:nth-of-type(1)>main:nth-of-type(1)>button:nth-of-type(2)',
  elementIndex: 1,
  ...overrides,
});

test('automatic heatmap clicks stay disabled unless opted in', async () => {
  const host = new FakeHeatmapHost();
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { heatmapHost: host },
    heatmapConfig(),
  );
  host.emit(heatmapSample());
  await pump();
  await client.flush();
  assert.equal(bodies.length, 0);
  assert.equal(host.size, 0);
  client.destroy();
});

test('auto heatmap clicks emit sanitized bounded properties', async () => {
  const host = new FakeHeatmapHost();
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { autoHeatmapClicks: true, heatmapHost: host },
    heatmapConfig(),
  );
  await client.init();
  host.emit(heatmapSample());
  await pump();
  await client.flush();
  const events = bodies.flatMap((raw) => (JSON.parse(raw) as Body).events);
  assert.equal(events.length, 1);
  assert.equal(events[0]!.event_key, 'sys.heatmap_click');
  assert.deepEqual(events[0]!.properties, {
    x_ratio: 0.5123,
    y_ratio: 0.3333,
    viewport_width: 1366,
    viewport_height: 900,
    element_path: 'body:nth-of-type(1)>main:nth-of-type(1)>button:nth-of-type(2)',
    element_index: 1,
  });
  client.destroy();
  assert.equal(host.size, 0, 'destroy must unsubscribe the document listener');
});

test('heatmap collection rejects malformed samples and caps at 120 per minute', async () => {
  const host = new FakeHeatmapHost();
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { autoHeatmapClicks: true, heatmapHost: host, maxBatchEvents: 50 },
    heatmapConfig(),
  );
  await client.init();
  host.emit(heatmapSample({ viewportWidth: 0 }));
  host.emit(heatmapSample({ elementPath: '' }));
  for (let i = 0; i < 121; i++) host.emit(heatmapSample({ xRatio: i / 120 }));
  await pump();
  await client.flush();
  const events = bodies.flatMap((raw) => (JSON.parse(raw) as Body).events);
  assert.equal(events.length, 120);
  assert.ok(events.every((event) => event.event_key === 'sys.heatmap_click'));
  client.destroy();
});

const encryptedContentConfig = (revision = 1): PublicConfig => {
  const base = config(revision);
  return {
    ...base,
    events: [
      ...base.events,
      {
        event_key: 'sys.content_chunk',
        schema_version: 1,
        schema: { type: 'object', additionalProperties: false, properties: {} },
      },
    ],
  };
};

const encryptedChunk = (overrides: Partial<EncryptedContentChunk> = {}): EncryptedContentChunk => ({
  contentType: 'ai_chat_message',
  streamId: 'stream-1',
  itemId: 'item-1',
  actorType: 'user',
  chunkIndex: 0,
  chunkCount: 1,
  ciphertext: 'ciphertext',
  nonce: 'nonce',
  wrappedKey: 'wrappedkey',
  keyId: 'key-1',
  alg: 'A256GCM+RSA-OAEP-256',
  aadVersion: 'evia-content-v1',
  padding: 'fixed-600-v1',
  contentLength: 12,
  truncated: false,
  ...overrides,
});

test('encrypted content uses the strict generic reserved envelope', async () => {
  const { client, bodies } = setupAuto((body) => receipt(body), {}, encryptedContentConfig());
  await client.init();
  const emission = client.emitEncryptedContentChunk(encryptedChunk());
  await pump();
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  const event = (JSON.parse(bodies[0]!) as Body).events[0]!;
  assert.equal(event.event_key, 'sys.content_chunk');
  assert.deepEqual(event.properties, {
    content_type: 'ai_chat_message',
    stream_id: 'stream-1',
    item_id: 'item-1',
    actor_type: 'user',
    chunk_index: 0,
    chunk_count: 1,
    ciphertext: 'ciphertext',
    nonce: 'nonce',
    wrapped_key: 'wrappedkey',
    key_id: 'key-1',
    alg: 'A256GCM+RSA-OAEP-256',
    aad_version: 'evia-content-v1',
    padding: 'fixed-600-v1',
    content_length: 12,
    truncated: false,
  });
  assert.throws(() => client.emit('sys.content_chunk'), /INVALID_CUSTOM_EVENT_KEY/);
  client.destroy();
});

test('4096-padded content stays within ciphertext and total event bounds', async () => {
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { maxQueueBytes: 8192 },
    encryptedContentConfig(),
  );
  await client.init();
  client.identify('account-123');
  client.navigate({
    routeName: 'replay detail',
    routeTemplate: '/projects/:projectId/replays/:replayId',
    sourceModule: 'replay-player',
    navigationType: 'push',
  });
  const emission = client.emitEncryptedContentChunk(
    encryptedChunk({
      contentType: 'session_replay_snapshot',
      streamId: 'replay-2026-11-23-0001',
      itemId: 'segment-000032',
      actorType: 'browser',
      ciphertext: 'a'.repeat(5483),
      nonce: 'b'.repeat(16),
      wrappedKey: 'c'.repeat(342),
      padding: 'fixed-4096-v2',
      contentLength: 4094,
      status: 'complete',
      durationMs: 1250,
    }),
  );
  await client.flush();
  assert.equal((await emission.result).status, 'accepted');
  const event = (JSON.parse(bodies[0]!) as Body).events[0]!;
  const eventBytes = new TextEncoder().encode(JSON.stringify(event)).byteLength;
  assert.ok(eventBytes < 8192, `realistic 4096-mode event used ${eventBytes} bytes`);
  assert.equal(event.properties.padding, 'fixed-4096-v2');
  assert.equal(event.properties.ciphertext.length, 5483);
  client.destroy();
});

test('encrypted content enforces mode-specific ciphertext bounds', () => {
  const { client } = setupAuto((body) => receipt(body), {}, encryptedContentConfig());
  assert.doesNotThrow(() =>
    client.emitEncryptedContentChunk(
      encryptedChunk({ padding: 'fixed-600-v1', ciphertext: 'a'.repeat(1024) }),
    ),
  );
  assert.throws(
    () =>
      client.emitEncryptedContentChunk(
        encryptedChunk({ padding: 'fixed-600-v1', ciphertext: 'a'.repeat(1025) }),
      ),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_ENCRYPTED_CONTENT_CHUNK',
  );
  client.destroy();

  const modern = setupAuto((body) => receipt(body), {}, encryptedContentConfig());
  assert.throws(
    () =>
      modern.client.emitEncryptedContentChunk(
        encryptedChunk({ padding: 'fixed-4096-v2', ciphertext: 'a'.repeat(5484) }),
      ),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_ENCRYPTED_CONTENT_CHUNK',
  );
  assert.equal(modern.client.pending, 0);
  modern.client.destroy();
});

test('encrypted content rejects malformed chunks before queue allocation', () => {
  const { client } = setupAuto((body) => receipt(body), {}, encryptedContentConfig());
  for (const malformed of [
    encryptedChunk({ chunkIndex: 1, chunkCount: 1 }),
    encryptedChunk({ ciphertext: 'not+base64' }),
    encryptedChunk({ contentLength: 0 }),
    encryptedChunk({ streamId: '' }),
    encryptedChunk({ padding: 'fixed-600-v2' as EncryptedContentChunk['padding'] }),
  ]) {
    assert.throws(
      () => client.emitEncryptedContentChunk(malformed),
      /INVALID_ENCRYPTED_CONTENT_CHUNK|INVALID_TEXT/,
    );
  }
  assert.equal(client.pending, 0);
  client.destroy();
});

test('bulk encrypted admission supports mixed modes at the 32-chunk item limit', async () => {
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { maxQueueEvents: 32 },
    encryptedContentConfig(),
  );
  const chunks = Array.from({ length: 32 }, (_, index) =>
    encryptedChunk({
      itemId: 'mixed-mode-item',
      chunkIndex: index,
      chunkCount: 32,
      padding: index % 2 === 0 ? 'fixed-600-v1' : 'fixed-4096-v2',
      ciphertext: 'a'.repeat(index % 2 === 0 ? 822 : 5483),
      contentLength: 32 * 4094,
    }),
  );
  const emissions = client.emitEncryptedContentChunks(chunks);
  const result = await client.drainUntilSettled(emissions, { timeoutMs: 5000 });
  assert.equal(result.status, 'accepted');
  const sent = bodies.flatMap((body) => (JSON.parse(body) as Body).events);
  assert.equal(sent.length, 32);
  assert.deepEqual(
    sent.map((event) => event.properties.chunk_index),
    Array.from({ length: 32 }, (_, index) => index),
  );
  assert.equal(sent.filter((event) => event.properties.padding === 'fixed-600-v1').length, 16);
  assert.equal(sent.filter((event) => event.properties.padding === 'fixed-4096-v2').length, 16);
  client.destroy();
});

test('bulk encrypted admission is atomic and drains more than four batches to own outcomes', async () => {
  const token = 't'.repeat(32);
  const cfg: PublicConfig = {
    ...encryptedContentConfig(7),
    telemetry_token: token,
    telemetry_expires_at: '2026-11-09T13:00:00Z',
  };
  const seenHeaders: string[] = [];
  const { client, bodies } = setupAuto(
    (body, init) => {
      seenHeaders.push((init.headers as Record<string, string>)['X-Evia-Telemetry']!);
      return receipt(body);
    },
    { maxBatchEvents: 2 },
    cfg,
  );
  const chunks = Array.from({ length: 11 }, (_, index) =>
    encryptedChunk({ itemId: `item-${index}` }),
  );
  const emissions = client.emitEncryptedContentChunks(chunks);
  assert.equal(client.pending, 11);
  const result = await client.drainUntilSettled(emissions, { timeoutMs: 5000 });

  assert.equal(result.status, 'accepted');
  assert.equal(result.reason, 'settled');
  assert.equal(result.rounds, 2);
  assert.equal(result.pendingEventIds.length, 0);
  assert.equal(result.outcomes.length, emissions.length);
  assert.throws(() => {
    (result.outcomes[0] as any).status = 'rejected';
  });
  assert.deepEqual(
    bodies.map((body) => (JSON.parse(body) as Body).events.length),
    [2, 2, 2, 2, 2, 1],
  );
  assert.deepEqual(
    bodies.flatMap((body) => (JSON.parse(body) as Body).events.map((event) => event.event_id)),
    emissions.map((emission) => emission.eventId),
  );
  assert.ok(seenHeaders.every((value) => value === token));
  assert.ok(
    bodies.every((body) =>
      (JSON.parse(body) as Body).events.every((event) => event.release_revision === 7),
    ),
  );
  client.destroy();
});

test('bulk admission rejects queue pressure without enqueuing a hidden prefix', async () => {
  const { client } = setupAuto(
    (body) => receipt(body),
    { maxQueueEvents: 3 },
    encryptedContentConfig(),
  );
  await client.init();
  const unrelated = client.emit('page_opened');
  assert.throws(
    () =>
      client.emitEncryptedContentChunks(
        Array.from({ length: 3 }, (_, index) => encryptedChunk({ itemId: `full-${index}` })),
      ),
    (error) => error instanceof EviaSDKError && error.code === 'QUEUE_FULL',
  );
  assert.equal(client.pending, 1);
  const admitted = client.emitEncryptedContentChunks([
    encryptedChunk({ itemId: 'fit-1' }),
    encryptedChunk({ itemId: 'fit-2' }),
  ]);
  const result = await client.drainUntilSettled(admitted);
  assert.equal(result.status, 'accepted');
  assert.equal((await unrelated.result).status, 'accepted');
  client.destroy();

  const bytes = setupAuto(
    (body) => receipt(body),
    { maxQueueEvents: 10, maxQueueBytes: 8192 },
    encryptedContentConfig(),
  );
  assert.throws(
    () =>
      bytes.client.emitEncryptedContentChunks(
        Array.from({ length: 4 }, (_, index) =>
          encryptedChunk({
            itemId: `byte-${index}`,
            ciphertext: 'a'.repeat(1024),
            wrappedKey: 'b'.repeat(1024),
          }),
        ),
      ),
    (error) => error instanceof EviaSDKError && error.code === 'QUEUE_FULL',
  );
  assert.equal(bytes.client.pending, 0);
  const sparse = new Array<EncryptedContentChunk>(1);
  assert.throws(
    () => bytes.client.emitEncryptedContentChunks(sparse),
    (error) => error instanceof EviaSDKError && error.code === 'INVALID_BULK_SIZE',
  );
  assert.equal(bytes.client.pending, 0);
  bytes.client.destroy();

  const idle = setupAuto(
    (body) => receipt(body),
    { maxQueueEvents: 2, autoSessionEnd: true, sessionTimeoutMs: 60000 },
    encryptedContentConfig(),
  );
  await idle.client.init();
  idle.client.emit('page_opened');
  idle.clock.time += 60000;
  assert.throws(
    () =>
      idle.client.emitEncryptedContentChunks([
        encryptedChunk({ itemId: 'idle-1' }),
        encryptedChunk({ itemId: 'idle-2' }),
      ]),
    (error) => error instanceof EviaSDKError && error.code === 'QUEUE_FULL',
  );
  assert.equal(idle.client.pending, 1, 'failed admission must not enqueue an idle session end');
  idle.client.destroy();
});

test('bulk drain max-round policy returns only its still-pending handles', async () => {
  const { client } = setupAuto(
    (body) => receipt(body),
    { maxBatchEvents: 2 },
    encryptedContentConfig(),
  );
  const emissions = client.emitEncryptedContentChunks(
    Array.from({ length: 9 }, (_, index) => encryptedChunk({ itemId: `round-${index}` })),
  );
  const bounded = await client.drainUntilSettled(emissions, { maxRounds: 1 });
  assert.equal(bounded.status, 'incomplete');
  assert.equal(bounded.reason, 'max_rounds');
  assert.equal(bounded.outcomes.length, 8);
  assert.deepEqual(bounded.pendingEventIds, [emissions[8]!.eventId]);
  assert.equal(client.pending, 1);

  const resumed = await client.drainUntilSettled(emissions);
  assert.equal(resumed.status, 'accepted');
  assert.equal(resumed.outcomes.length, 9);
  client.destroy();
});

test('bulk drain honors Retry-After and does not consume rounds while throttled', async () => {
  let calls = 0;
  const { client, clock, bodies } = setupAuto(
    (body) =>
      ++calls === 1
        ? new Response(null, { status: 429, headers: { 'Retry-After': '2' } })
        : receipt(body),
    {},
    encryptedContentConfig(),
  );
  const emissions = client.emitEncryptedContentChunks([encryptedChunk()]);
  const draining = client.drainUntilSettled(emissions, { timeoutMs: 5000, maxRounds: 4 });
  await pump();
  assert.equal(bodies.length, 1);
  await clock.advance(1999);
  assert.equal(bodies.length, 1);
  await clock.advance(1);
  const result = await draining;
  assert.equal(result.status, 'accepted');
  assert.equal(result.reason, 'settled');
  assert.equal(result.rounds, 2);
  assert.equal(bodies.length, 2);
  client.destroy();
});

test('bulk drain never accepts an ambiguous then definitive failure', async () => {
  let calls = 0;
  const { client, clock, bodies } = setupAuto(
    (body) =>
      ++calls === 1
        ? json({ batch_id: body.batch_id, receipt_id: receiptId, results: [] }, 202)
        : new Response(null, { status: 400 }),
    { maxAttempts: 2 },
    encryptedContentConfig(),
  );
  const emissions = client.emitEncryptedContentChunks([encryptedChunk()]);
  const draining = client.drainUntilSettled(emissions, { timeoutMs: 1000 });
  await pump();
  await clock.advance(10);
  const result = await draining;
  assert.equal(result.status, 'incomplete');
  assert.equal(result.reason, 'settled');
  assert.equal(result.pendingEventIds.length, 0);
  assert.equal(result.outcomes[0]!.status, 'uncertain');
  assert.equal(bodies.length, 2);
  assert.equal(bodies[0], bodies[1]);
  client.destroy();
});

test('bulk drain timeout and cancellation retain pending queue work', async () => {
  const timed = setupAuto(
    () => new Response(null, { status: 429, headers: { 'Retry-After': '2' } }),
    {},
    encryptedContentConfig(),
  );
  const timedEmissions = timed.client.emitEncryptedContentChunks([encryptedChunk()]);
  const timingOut = timed.client.drainUntilSettled(timedEmissions, { timeoutMs: 50 });
  await pump();
  await timed.clock.advance(50);
  const timedResult = await timingOut;
  assert.equal(timedResult.status, 'incomplete');
  assert.equal(timedResult.reason, 'deadline');
  assert.deepEqual(timedResult.pendingEventIds, [timedEmissions[0]!.eventId]);
  assert.equal(timed.client.pending, 1);
  timed.client.destroy();

  const cancelled = setupAuto(() => new Promise<Response>(() => {}), {}, encryptedContentConfig());
  const cancelledEmissions = cancelled.client.emitEncryptedContentChunks([encryptedChunk()]);
  const controller = new AbortController();
  const cancelling = cancelled.client.drainUntilSettled(cancelledEmissions, {
    timeoutMs: 1000,
    signal: controller.signal,
  });
  await pump();
  controller.abort();
  const cancelledResult = await cancelling;
  assert.equal(cancelledResult.status, 'incomplete');
  assert.equal(cancelledResult.reason, 'aborted');
  assert.deepEqual(cancelledResult.pendingEventIds, [cancelledEmissions[0]!.eventId]);
  assert.equal(cancelled.client.pending, 1);
  cancelled.client.destroy();
});

test('concurrent bulk drains coalesce collection flights without losing either caller', async () => {
  const { client, bodies } = setupAuto(
    (body) => receipt(body),
    { maxBatchEvents: 1 },
    encryptedContentConfig(),
  );
  const emissions = client.emitEncryptedContentChunks(
    Array.from({ length: 6 }, (_, index) => encryptedChunk({ itemId: `concurrent-${index}` })),
  );
  const [first, second] = await Promise.all([
    client.drainUntilSettled(emissions.slice(0, 3)),
    client.drainUntilSettled(emissions.slice(3)),
  ]);
  assert.equal(first.status, 'accepted');
  assert.equal(second.status, 'accepted');
  assert.equal(bodies.length, 6);
  assert.equal(
    new Set(bodies.map((body) => (JSON.parse(body) as Body).events[0]!.event_id)).size,
    6,
  );
  client.destroy();
});

test('bulk drain success is scoped to provided emissions and retains unrelated retries', async () => {
  const { client } = setupAuto(
    (body) =>
      body.events[0]!.event_key === 'sys.content_chunk'
        ? receipt(body)
        : new Response(null, { status: 429, headers: { 'Retry-After': '2' } }),
    { maxBatchEvents: 1 },
    encryptedContentConfig(),
  );
  const own = client.emitEncryptedContentChunks([encryptedChunk()]);
  const unrelated = client.emit('page_opened');
  const result = await client.drainUntilSettled(own, { timeoutMs: 1000 });
  assert.equal(result.status, 'accepted');
  assert.deepEqual(
    result.outcomes.map((outcome) => outcome.eventId),
    [own[0]!.eventId],
  );
  assert.equal(client.pending, 1);
  let unrelatedSettled = false;
  void unrelated.result.then(() => {
    unrelatedSettled = true;
  });
  await pump();
  assert.equal(unrelatedSettled, false);
  client.destroy();
});

test('persisted bulk queue survives reload with stable ids and retry state', async () => {
  const storage = new MapStorage();
  const first = setupAuto(
    () => new Response(null, { status: 503 }),
    { storage, persistQueue: true, maxAttempts: 2 },
    encryptedContentConfig(),
  );
  await first.client.init();
  const emissions = first.client.emitEncryptedContentChunks([
    encryptedChunk({ itemId: 'persist-1' }),
    encryptedChunk({ itemId: 'persist-2' }),
  ]);
  await pump();
  await first.client.flush();
  const afterAttempt = JSON.parse(storage.read(queueStorageKey)!);
  assert.deepEqual(
    afterAttempt.entries.map((entry: any) => [entry.eventId, entry.attempts, entry.ambiguous]),
    emissions.map((emission) => [emission.eventId, 1, true]),
  );

  const second = setupAuto(
    (body) => receipt(body),
    { storage, persistQueue: true, maxAttempts: 2 },
    encryptedContentConfig(),
  );
  assert.equal(second.client.pending, 2);
  await second.client.init();
  await pump();
  await second.client.flush();
  const delivered = second.bodies.flatMap((body) =>
    (JSON.parse(body) as Body).events.map((event) => event.event_id),
  );
  assert.deepEqual(
    delivered,
    emissions.map((emission) => emission.eventId),
  );
  assert.equal(JSON.parse(storage.read(queueStorageKey)!).entries.length, 0);
  second.client.destroy();
});

test('reload never exceeds max attempts after an in-flight persisted submission', async () => {
  const storage = new MapStorage();
  let complete!: (response: Response) => void;
  const first = setupAuto(
    () =>
      new Promise<Response>((resolve) => {
        complete = resolve;
      }),
    { storage, persistQueue: true, maxAttempts: 1 },
    encryptedContentConfig(),
  );
  await first.client.init();
  const emissions = first.client.emitEncryptedContentChunks([encryptedChunk()]);
  const firstFlush = first.client.flush();
  await pump();
  assert.equal(first.bodies.length, 1);
  const inFlight = JSON.parse(storage.read(queueStorageKey)!);
  assert.equal(inFlight.entries[0].eventId, emissions[0]!.eventId);
  assert.equal(inFlight.entries[0].attempts, 1);
  assert.equal(inFlight.entries[0].ambiguous, true);
  assert.equal(inFlight.entries[0].retryCode, 'RETRY_EXHAUSTED');

  const second = setupAuto(
    (body) => receipt(body),
    { storage, persistQueue: true, maxAttempts: 1 },
    encryptedContentConfig(),
  );
  assert.equal(second.client.pending, 1);
  await second.client.init();
  await pump();
  await second.client.flush();
  assert.equal(second.bodies.length, 0, 'the reloaded client must not submit a second attempt');
  assert.equal(second.client.pending, 0);
  assert.equal(JSON.parse(storage.read(queueStorageKey)!).entries.length, 0);

  complete(receipt(JSON.parse(first.bodies[0]!) as Body));
  await firstFlush;
  second.client.destroy();
});
