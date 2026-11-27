import { randomUUID } from 'node:crypto';
import { readdir, readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { expect, test, type BrowserContext } from '@playwright/test';
import type { BrowserClient, Envelope } from '../../packages/browser-sdk/src/types';
import { loginAsE2EUser } from './auth-session';

type SDKWindow = Window & { eviaContinuityClient?: BrowserClient };
type StoredEvent = { event_id: string; context: Envelope['context'] };

// Credential responses must never enter automatic trace or screenshot artifacts.
test.use({ trace: 'off', screenshot: 'off', video: 'off', reducedMotion: 'reduce' });

test('persistSession keeps one session across a hard reload and lands in the warehouse', async ({
  page,
}) => {
  test.skip(
    process.env.EVIA_E2E_FULL_STACK !== '1',
    'Requires EVIA_E2E_FULL_STACK=1 and real API, ingest/query workers, PostgreSQL, Kafka, ClickHouse and Redis.',
  );
  test.setTimeout(180_000);
  page.setDefaultTimeout(12_000);
  await page.setViewportSize({ width: 1366, height: 900 });
  await loginAsE2EUser(page);
  const managementOrigin = new URL(page.url()).origin;
  const sessionResponse = await page.request.get('/api/v1/session');
  expect(sessionResponse.status()).toBe(200);
  const { csrf_token: csrf } = (await sessionResponse.json()) as { csrf_token: string };
  const headers = { Origin: managementOrigin, 'X-CSRF-Token': csrf };
  const stamp = randomUUID();
  // The committed vite proxy targets 127.0.0.1:8080; hosts that cannot bind it
  // run the API elsewhere and export EVIA_E2E_API for the synthetic site.
  const endpoint = process.env.EVIA_E2E_API ?? 'http://127.0.0.1:8080';

  const created = await page.request.post('/api/v1/projects', {
    headers,
    data: { name: `SDK session continuity ${stamp}`, timezone: 'UTC' },
  });
  expect(created.status()).toBe(201);
  const project = (await created.json()) as { id: string };
  const base = `/api/v1/projects/${project.id}`;
  const definition = {
    display_name: 'SDK 会话操作',
    trigger: '合成站点的会话连续性按钮操作。',
    allowed_source_kinds: ['browser'],
    fact_kind: 'client_observed',
    schema: {
      type: 'object',
      additionalProperties: false,
      properties: { page: { type: 'string', minLength: 1, maxLength: 128 } },
      required: ['page'],
    },
    example_properties: { page: 'synthetic' },
  };
  const defined = await page.request.post(`${base}/event-definitions`, {
    headers,
    data: { event_key: 'browser_action', display_name: definition.display_name },
  });
  expect(defined.status()).toBe(201);
  const initial = (await (
    await page.request.get(`${base}/event-definitions/browser_action/draft`, { headers })
  ).json()) as { revision: number };
  const drafted = await page.request.patch(`${base}/event-definitions/browser_action/draft`, {
    headers: { ...headers, 'If-Match': `"${initial.revision}"` },
    data: definition,
  });
  expect(drafted.status()).toBe(200);
  const draft = (await drafted.json()) as { revision: number };
  const validated = await page.request.post(`${base}/event-definitions/browser_action/validate`, {
    headers,
    data: { draft_revision: draft.revision, sample_properties: definition.example_properties },
  });
  expect(validated.status()).toBe(200);
  expect(((await validated.json()) as { valid: boolean }).valid).toBe(true);
  const releaseKey = `session-continuity-${stamp}`;
  const published = await page.request.post(`${base}/releases`, {
    headers: { ...headers, 'Idempotency-Key': releaseKey },
    data: {
      environment: 'development',
      expected_active_revision: 0,
      events: [{ event_key: 'browser_action', draft_revision: draft.revision }],
    },
  });
  expect([201, 202]).toContain(published.status());
  const release = (await published.json()) as { release_id: string; ingestion_ready: boolean };
  for (let attempt = 0; !release.ingestion_ready && attempt < 20; attempt++) {
    await delay(500);
    const polled = (await (
      await page.request.get(`${base}/releases/${release.release_id}`, { headers })
    ).json()) as { ingestion_ready: boolean };
    release.ingestion_ready = polled.ingestion_ready;
  }
  expect(release.ingestion_ready).toBe(true);
  const activated = await page.request.post(`${base}/release-activations`, {
    headers: { ...headers, 'Idempotency-Key': `activate-${stamp}` },
    data: {
      environment: 'development',
      release_id: release.release_id,
      expected_active_revision: 0,
    },
  });
  expect(activated.status()).toBe(200);

  // Serve the compiled SDK from a synthetic origin, exactly like a business site.
  const sdkDir = resolve('packages/browser-sdk/dist');
  const modules = new Map(
    await Promise.all(
      (await readdir(sdkDir))
        .filter((name) => name.endsWith('.js'))
        .map(async (name) => [name, await readFile(resolve(sdkDir, name))] as const),
    ),
  );
  const server = createServer((request, response) => {
    response.setHeader('Cache-Control', 'no-store');
    if (request.method !== 'GET') {
      response.writeHead(405).end();
    } else if (request.url === '/') {
      response.writeHead(200, { 'Content-Type': 'text/html;charset=utf-8' });
      response.end(
        '<!doctype html><html lang="en"><title>SDK session site</title><body><h1>SDK synthetic site</h1><pre id="scope"></pre></body></html>',
      );
    } else {
      const content = modules.get(request.url?.slice(1) ?? '');
      if (!content) response.writeHead(404).end();
      else {
        response.writeHead(200, { 'Content-Type': 'application/javascript;charset=utf-8' });
        response.end(content);
      }
    }
  });
  await new Promise<void>((ready, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject);
      ready();
    });
  });
  let context: BrowserContext | undefined;
  const marker = `session-${stamp}`;
  try {
    const address = server.address();
    if (!address || typeof address === 'string') throw new Error('synthetic server has no port');
    const origin = `http://127.0.0.1:${address.port}`;
    const sourceResponse = await page.request.post(`${base}/sources`, {
      headers,
      data: {
        name: `SDK session continuity ${stamp}`,
        environment: 'development',
        kind: 'browser',
        allowed_origins: [origin],
      },
    });
    expect(sourceResponse.status()).toBe(201);
    const source = (await sourceResponse.json()) as { id: string; write_key: string };
    context = await page.context().browser()!.newContext({ serviceWorkers: 'block' });
    const sdkPage = await context.newPage();
    const errors: string[] = [];
    sdkPage.on('pageerror', (error) => errors.push(error.message));
    const clientOptions = {
      endpoint,
      projectId: project.id,
      environment: 'development' as const,
      writeKey: source.write_key,
      persistSession: true,
      lifecycle: false,
      flushIntervalMs: 500,
      retryBaseMs: 50,
      requestTimeoutMs: 5000,
    };
    type Scope = { session_id: string; tab_id: string; sequence: number };
    const drive = async (label: string): Promise<Scope> => {
      const outcome = await sdkPage.evaluate(
        async ({ moduleURL, options, label, marker: value }) => {
          const { createEviaClient } = await import(moduleURL);
          const client: BrowserClient = createEviaClient(options);
          (window as SDKWindow).eviaContinuityClient = client;
          const configuration = await client.init();
          if (configuration.project_id !== options.projectId)
            throw new Error('unexpected configuration project');
          const emission = client.emit('browser_action', { page: value });
          await client.flush();
          const settled = await emission.result;
          if (settled.status !== 'accepted') throw new Error(`delivery settled ${settled.status}`);
          const key = Object.keys(sessionStorage).find((candidate) =>
            candidate.endsWith(':session'),
          );
          const scope = key
            ? (JSON.parse(sessionStorage.getItem(key)!) as {
                session_id: string;
                tab_id: string;
                sequence: number;
              })
            : null;
          const statusLine = document.getElementById('scope')!;
          statusLine.textContent = `${JSON.stringify(
            { document: label, event_id: emission.eventId, scope },
            null,
            2,
          )}\n${statusLine.textContent ?? ''}`;
          return scope;
        },
        { moduleURL: `${origin}/index.js`, options: clientOptions, label, marker },
      );
      if (!outcome) throw new Error('no mirrored session snapshot after delivery');
      return outcome;
    };
    const started = Date.now();
    await sdkPage.goto(origin);
    const first = await drive('before-reload');
    await sdkPage.reload();
    const second = await drive('after-reload');
    expect(errors).toEqual([]);
    expect(second.session_id).toBe(first.session_id);
    expect(second.tab_id).toBe(first.tab_id);
    expect(second.sequence).toBeGreaterThan(first.sequence);
    // A second tab in the same browser context owns a separate sessionStorage,
    // so it must start an isolated session rather than join the first tab's.
    const otherTab = await context.newPage();
    await otherTab.goto(origin);
    const third = await otherTab.evaluate(
      async ({ moduleURL, options, marker: value }) => {
        const { createEviaClient } = await import(moduleURL);
        const client: BrowserClient = createEviaClient(options);
        await client.init();
        const emission = client.emit('browser_action', { page: value });
        await client.flush();
        await emission.result;
        const key = Object.keys(sessionStorage).find((candidate) => candidate.endsWith(':session'));
        return key ? (JSON.parse(sessionStorage.getItem(key)!) as { session_id: string }) : null;
      },
      { moduleURL: `${origin}/index.js`, options: clientOptions, marker: `${marker}-other` },
    );
    expect(third!.session_id).not.toBe(first.session_id);

    // Only a real warehouse read establishes cross-reload visibility.
    const common = {
      environment: 'development',
      timezone: 'UTC',
      time_range: {
        from: new Date(started - 5000).toISOString(),
        to: new Date(new Date().getTime() + 5000).toISOString(),
      },
      event_versions: [{ event_key: 'browser_action', schema_version: 1 }],
      filter: {
        op: 'eq',
        field: { kind: 'property', event_key: 'browser_action', schema_version: 1, key: 'page' },
        value: marker,
      },
      limit: 20,
    };
    const submitted = await page.request.post(`${base}/queries`, {
      headers,
      data: { ...common, kind: 'events' },
    });
    expect(submitted.status()).toBe(202);
    const job = (await submitted.json()) as { query_id: string };
    let stored: StoredEvent[] = [];
    for (let attempt = 0; attempt < 10; attempt++) {
      const polled = (await (
        await page.request.get(`${base}/queries/${job.query_id}`, { headers })
      ).json()) as { state: string; error?: { code: string } };
      if (polled.state === 'succeeded') {
        const result = await page.request.get(`${base}/queries/${job.query_id}/result`, {
          headers,
        });
        stored = ((await result.json()) as { rows: StoredEvent[] }).rows;
        break;
      }
      if (['failed', 'canceled', 'expired'].includes(polled.state))
        throw new Error(`session continuity query ended ${polled.state}: ${polled.error?.code}`);
      await delay(500);
    }
    expect(stored).toHaveLength(2);
    expect(stored.map((event) => event.context.session_id)).toEqual([
      first.session_id,
      first.session_id,
    ]);
    // The warehouse observes one continued session with strictly increasing
    // sequence numbers across the hard reload boundary.
    expect(stored[1]!.context.sequence).toBeGreaterThan(stored[0]!.context.sequence);
    expect(stored.every((event) => event.context.tab_id === first.tab_id)).toBe(true);
  } finally {
    await context?.close();
    await new Promise<void>((done) => server.close(() => done()));
  }
});
