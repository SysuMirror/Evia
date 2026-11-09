import { randomUUID } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { expect, type BrowserContext, type Page } from '@playwright/test';
import type { BrowserClient, Envelope } from '../../packages/browser-sdk/src/types';

type SDKWindow = Window & { eviaWorkflowClient?: BrowserClient };
type StoredEvent = {
  event_id: string;
  source_id: string;
  uid: string | null;
  anonymous_id: string | null;
  context: Envelope['context'];
  properties: { page: string };
};

/** Extends the core workflow after its original three-event assertions finish. */
export async function verifyBrowserSDKWorkflow(page: Page, projectID: string): Promise<void> {
  const browser = page.context().browser();
  if (!browser) throw new Error('SDK workflow requires a browser instance');
  // Never serve arbitrary repository files, source maps, credentials or paths.
  const modules = new Map(
    await Promise.all(
      ['index.js', 'client.js', 'transport.js', 'validation.js', 'types.js'].map(
        async (name) => [name, await readFile(resolve('packages/browser-sdk/dist', name))] as const,
      ),
    ),
  );
  const server = createServer((request, response) => {
    response.setHeader('Cache-Control', 'no-store');
    if (request.method !== 'GET') {
      response.writeHead(405).end();
    } else if (request.url === '/') {
      response.writeHead(200, { 'Content-Type': 'text/html;charset=utf-8' });
      response.end(
        '<!doctype html><html lang="en"><title>SDK synthetic site</title><body>SDK synthetic site</body></html>',
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
  try {
    const address = server.address();
    if (!address || typeof address === 'string')
      throw new Error('SDK synthetic server has no port');
    const origin = `http://127.0.0.1:${address.port}`;
    const managementOrigin = new URL(page.url()).origin;
    const sessionResponse = await page.request.get('/api/v1/session');
    expect(sessionResponse.status()).toBe(200);
    const session = (await sessionResponse.json()) as { csrf_token: string };
    const headers = { Origin: managementOrigin, 'X-CSRF-Token': session.csrf_token };
    const base = `/api/v1/projects/${encodeURIComponent(projectID)}`;
    const run = randomUUID();
    const created = await page.request.post(`${base}/sources`, {
      headers,
      data: {
        name: `SDK synthetic ${run}`,
        environment: 'development',
        kind: 'browser',
        allowed_origins: [origin],
      },
    });
    expect(created.status()).toBe(201);
    const source = (await created.json()) as { id: string; write_key: string; revision: number };
    context = await browser.newContext({ serviceWorkers: 'block' });
    expect(await context.cookies()).toEqual([]);
    const sdkPage = await context.newPage();
    const pageErrors: string[] = [];
    sdkPage.on('pageerror', (error) => pageErrors.push(error.message));
    const bodies: string[] = [];
    let configReads = 0;
    sdkPage.on('response', (response) => {
      if (response.url().startsWith('http://127.0.0.1:8080/v1/config?')) configReads++;
    });
    await sdkPage.route('http://127.0.0.1:8080/v1/collect', async (route) => {
      const request = route.request();
      const requestHeaders = await request.allHeaders();
      expect(requestHeaders.origin).toBe(origin);
      expect('cookie' in requestHeaders).toBe(false);
      expect('authorization' in requestHeaders).toBe(false);
      const body = request.postData();
      if (body === null) throw new Error('SDK collection omitted its body');
      bodies.push(body);
      if (bodies.length === 1) {
        // Execute the real durable write, then interrupt only response delivery.
        const committed = await route.fetch();
        expect(committed.status()).toBe(202);
        const receipt = (await committed.json()) as { results: { status: string }[] };
        expect(receipt.results.map((item) => item.status)).toEqual(Array(5).fill('accepted'));
        await committed.dispose();
        await route.abort('failed');
      } else await route.continue();
    });
    await sdkPage.goto(origin);
    const started = Date.now();
    const marker = `sdk-${run}`;
    const uid = `sdk-user-${run}`;
    const emitted = await sdkPage.evaluate(
      async ({ moduleURL, projectID, writeKey, marker, uid }) => {
        const { createEviaClient } = await import(moduleURL);
        const client: BrowserClient = createEviaClient({
          endpoint: 'http://127.0.0.1:8080',
          projectId: projectID,
          environment: 'development',
          writeKey,
          storage: null,
          lifecycle: false,
          flushIntervalMs: 1000,
          retryBaseMs: 50,
          requestTimeoutMs: 5000,
          maxAttempts: 3,
        });
        (window as SDKWindow).eviaWorkflowClient = client;
        const configuration = await client.init();
        const initial = client.navigate({ routeName: 'SDK Home', navigationType: 'initial' });
        const anonymous = client.emit('browser_action', { page: marker });
        client.identify(uid);
        const link = client.beginNavigation();
        const navigation = client.emit('browser_action', { page: marker }, { navigation: link });
        const destination = client.navigate({ routeName: 'SDK Destination', link });
        const identified = client.emit('browser_action', { page: marker });
        client.reset();
        const reset = client.emit('browser_action', { page: marker });
        const finalAnonymous = client.emit('browser_action', { page: marker });
        const emissions = [anonymous, navigation, identified, reset, finalAnonymous];
        await client.flush();
        const outcomes = await Promise.all(emissions.map((emission) => emission.result));
        return {
          projectID: configuration.project_id,
          environment: configuration.environment,
          ids: emissions.map((emission) => emission.eventId),
          outcomes,
          initial,
          destination,
          link,
          immutableContext:
            Object.isFrozen(initial) && Object.isFrozen(destination) && Object.isFrozen(link),
          pending: client.pending,
        };
      },
      { moduleURL: `${origin}/index.js`, projectID, writeKey: source.write_key, marker, uid },
    );
    expect(emitted.projectID).toBe(projectID);
    expect(emitted.environment).toBe('development');
    expect(emitted.outcomes.map((outcome) => outcome.status)).toEqual(Array(5).fill('accepted'));
    expect(emitted.outcomes.every((outcome) => outcome.attempts === 2 && !!outcome.receiptId)).toBe(
      true,
    );
    expect(emitted.pending).toBe(0);
    expect(emitted.immutableContext).toBe(true);
    expect(configReads).toBeGreaterThan(0);
    expect(bodies).toHaveLength(2);
    // Compare as a boolean so failure output cannot print the write credential.
    expect(bodies[0] === bodies[1], 'response loss replays exactly the same batch bytes').toBe(
      true,
    );
    const sent = (JSON.parse(bodies[0]!) as { events: Envelope[] }).events;
    expect(sent.map((event) => event.event_id)).toEqual(emitted.ids);
    const expected = new Map(sent.map((event) => [event.event_id, event]));
    const common = {
      environment: 'development',
      timezone: 'UTC',
      time_range: {
        from: new Date(started - 5000).toISOString(),
        to: new Date(Date.now() + 5000).toISOString(),
      },
      event_versions: [{ event_key: 'browser_action', schema_version: 1 }],
      filter: {
        op: 'eq',
        field: { kind: 'property', event_key: 'browser_action', schema_version: 1, key: 'page' },
        value: marker,
      },
      limit: 20,
    };
    async function query(request: Record<string, unknown>): Promise<StoredEvent[]> {
      const submitted = await page.request.post(`${base}/queries`, { headers, data: request });
      expect(submitted.status()).toBe(202);
      const job = (await submitted.json()) as { query_id: string };
      const deadline = Date.now() + 20_000;
      while (Date.now() < deadline) {
        const polled = await page.request.get(`${base}/queries/${job.query_id}`);
        expect(polled.status()).toBe(200);
        const current = (await polled.json()) as { state: string; error?: { code: string } };
        if (current.state === 'succeeded') {
          const result = await page.request.get(`${base}/queries/${job.query_id}/result`);
          expect(result.status()).toBe(200);
          return ((await result.json()) as { rows: StoredEvent[] }).rows;
        }
        if (['failed', 'canceled', 'expired'].includes(current.state)) {
          throw new Error(
            `SDK verification query ended ${current.state}: ${current.error?.code ?? 'unknown'}`,
          );
        }
        await delay(200);
      }
      throw new Error('SDK verification query did not finish within its bounded poll window');
    }
    // A receipt proves queue admission. Only these actual queries establish visibility.
    let stored: StoredEvent[] = [];
    for (let attempt = 0; attempt < 4; attempt++) {
      stored = await query({ ...common, kind: 'events' });
      expect(stored.length).toBeLessThanOrEqual(5);
      if (stored.length === 5) break;
      await delay(500);
    }
    expect(stored).toHaveLength(5);
    expect(new Set(stored.map((event) => event.event_id)).size).toBe(5);
    expect(stored.map((event) => event.event_id).sort()).toEqual([...emitted.ids].sort());
    for (const event of stored) {
      const original = expected.get(event.event_id)!;
      expect(event.source_id).toBe(source.id);
      expect(event.uid).toBe(original.identity.uid ?? null);
      expect(event.anonymous_id).toBe(original.identity.anonymous_id);
      expect(event.context).toEqual(original.context);
      expect(event.properties.page).toBe(marker);
    }
    expect(sent[0]!.identity.uid).toBeUndefined();
    expect(sent[1]!.identity.uid).toBe(uid);
    expect(sent[2]!.identity.uid).toBe(uid);
    expect(sent[3]!.identity.uid).toBeUndefined();
    expect(sent[0]!.identity.anonymous_id).toBe(sent[1]!.identity.anonymous_id);
    expect(sent[2]!.identity.anonymous_id === sent[3]!.identity.anonymous_id).toBe(false);
    expect(sent[2]!.context.session_id === sent[3]!.context.session_id).toBe(false);
    expect(sent[1]!.context.page_view_id).toBe(emitted.initial.page_view_id);
    expect(sent[1]!.context.navigation_id).toBe(emitted.link.navigationId);
    expect(sent[2]!.context.navigation_id).toBe(emitted.link.navigationId);
    expect(sent[2]!.context.source_page_view_id).toBe(emitted.initial.page_view_id);
    expect(sent[2]!.context.previous_page_view_id).toBe(emitted.initial.page_view_id);
    expect(sent[2]!.context.page_view_id).toBe(emitted.destination.page_view_id);
    expect(sent.map((event) => event.context.sequence)).toEqual([1, 2, 3, 4, 5]);
    const journey = await query({
      ...common,
      kind: 'journey',
      subject: 'uid',
      identity_filter: { uid },
    });
    expect(journey.map((event) => event.event_id).sort()).toEqual(
      [emitted.ids[1], emitted.ids[2]].sort(),
    );
    const anonymousJourney = await query({
      ...common,
      kind: 'journey',
      subject: 'anonymous',
      identity_filter: { anonymous_id: sent[0]!.identity.anonymous_id },
    });
    expect(anonymousJourney.map((event) => event.event_id).sort()).toEqual(
      emitted.ids.slice(0, 3).sort(),
    );
    const disabled = await page.request.patch(`${base}/sources/${source.id}`, {
      headers: { ...headers, 'If-Match': `"${source.revision}"` },
      data: { status: 'disabled' },
    });
    expect(disabled.status()).toBe(200);
    const publicHTTP = context.request;
    await expect
      .poll(
        async () => {
          try {
            const response = await publicHTTP.get(
              `http://127.0.0.1:8080/v1/config?write_key=${encodeURIComponent(source.write_key)}`,
              { headers: { Origin: origin }, timeout: 3000 },
            );
            const status = response.status();
            const problem = (await response.json()) as { code?: string };
            await response.dispose();
            return status === 403 && problem.code === 'SOURCE_DISABLED';
          } catch {
            // Do not expose a credential-bearing config URL in request failure logs.
            return false;
          }
        },
        { timeout: 10_000, intervals: [250, 500, 1000] },
      )
      .toBe(true);
    // CORS can hide a revoked source's 403 response (including its preflight).
    // Verify browser fail-closed behavior separately from the actual HTTP status.
    const refreshRejected = await sdkPage.evaluate(async () => {
      try {
        await (window as SDKWindow).eviaWorkflowClient!.refreshConfig();
        return false;
      } catch {
        return true;
      }
    });
    expect(refreshRejected).toBe(true);
    const revoked = await sdkPage.evaluate(async (marker) => {
      const client = (window as SDKWindow).eviaWorkflowClient!;
      return client.emit('browser_action', { page: marker }).result;
    }, marker);
    expect(revoked.status).toBe('dropped');
    expect(['CONFIG_HTTP_403', 'CONFIG_UNAVAILABLE'].includes(revoked.code ?? '')).toBe(true);
    expect(bodies).toHaveLength(2);
    expect(await context.cookies()).toEqual([]);
    expect(pageErrors, 'no uncaught SDK browser exceptions').toEqual([]);
  } finally {
    if (context) {
      for (const current of context.pages()) {
        await current
          .evaluate(() => (window as SDKWindow).eviaWorkflowClient?.destroy())
          .catch(() => {});
      }
      await context.close();
    }
    await new Promise<void>((closed, reject) => {
      server.close((error) => (error ? reject(error) : closed()));
      server.closeAllConnections();
    });
  }
}
