import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { chmod, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { promisify } from 'node:util';
import { expect, type BrowserContext, type Page } from '@playwright/test';
import {
  api,
  type Draft,
  type Release,
  type Source,
  type QueryJob,
  type QueryResult,
} from '../../packages/api-client/src/index';
import type { BrowserClient, Envelope } from '../../packages/browser-sdk/src/types';

const execute = promisify(execFile);
type SDKWindow = Window & { machineIntegrationClient?: BrowserClient };

/** Uses the human session only for project/credential administration. Every
 * integration operation uses the shipped Python helper and its machine bearer. */
export async function verifyMachineIntegrationWorkflow(page: Page): Promise<void> {
  const browser = page.context().browser();
  if (!browser) throw new Error('Machine integration requires a browser instance');
  const privateDirectory = await mkdtemp(join(tmpdir(), 'evia-machine-integration-'));
  await chmod(privateDirectory, 0o700);
  let context: BrowserContext | undefined;
  let closeSite: (() => Promise<void>) | undefined;
  const stamp = randomUUID();
  const managementOrigin = new URL(page.url()).origin;
  const endpoint = 'http://127.0.0.1:8080';
  // The shared client uses browser-relative URLs. Adapt its actual fetch transport
  // to this page's owner cookie jar for these short, sequential administration calls.
  async function human<T>(operation: () => Promise<T>): Promise<T> {
    const original = globalThis.fetch;
    const cookies = await page.context().cookies(managementOrigin);
    globalThis.fetch = async (input, options) => {
      const url = new URL(String(input), managementOrigin);
      if (url.origin !== managementOrigin) throw new Error('Unexpected human client origin');
      const headers = new Headers(options?.headers);
      headers.set('Origin', managementOrigin);
      headers.set('Cookie', cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join('; '));
      return original(url, { ...options, headers });
    };
    try {
      return await operation();
    } finally {
      globalThis.fetch = original;
    }
  }
  try {
    const session = await human(() => api.session());
    const project = await human(() =>
      api.createProject(
        { name: `Machine integration ${stamp}`, timezone: 'UTC' },
        session.csrf_token,
      ),
    );
    const issued = await human(() =>
      api.createMachinePrincipal(
        project.id,
        {
          name: `Activity helper ${stamp}`,
          environment: 'development',
          permissions: [
            'project.read',
            'catalog.drafts.read',
            'catalog.drafts.write',
            'catalog.releases.read',
            'catalog.releases.publish',
            'catalog.releases.activate',
            'sources.read',
            'sources.write',
            'queries.read',
            'queries.run',
            'queries.raw.read',
          ],
          expires_at: new Date(Date.now() + 3_600_000).toISOString(),
        },
        `machine-integration-${stamp}`,
        session.csrf_token,
      ),
    );
    if (!issued.secret_available || !issued.secret_once)
      throw new Error('Machine credential was not issued once');
    const token = issued.secret_once;
    const base = `/api/v1/projects/${project.id}`;
    const deadline = Date.now() + 100_000;
    let calls = 0;
    const scope = [
      '--endpoint',
      endpoint,
      '--allow-loopback-http',
      '--project',
      project.id,
      '--environment',
      'development',
    ];
    async function helper<T>(
      path?: string,
      method = 'GET',
      body?: unknown,
      revision?: number,
      key?: string,
    ): Promise<T> {
      calls++;
      if (calls > 96 || Date.now() >= deadline)
        throw new Error('Machine helper verification budget exhausted');
      const output = join(privateDirectory, `response-${calls}.json`);
      const args = ['integrations/tooling/activity_api.py', ...scope, '--output', output];
      if (path) args.push('--path', path, '--method', method);
      if (body !== undefined) {
        const input = join(privateDirectory, `request-${calls}.json`);
        await writeFile(input, JSON.stringify(body), { mode: 0o600 });
        args.push('--body-file', input);
      }
      if (revision !== undefined) args.push('--if-match', `"${revision}"`);
      if (key) args.push('--idempotency-key', key);
      try {
        const result = await execute('python3', args, {
          env: { ...process.env, EVIA_MACHINE_TOKEN: token },
          timeout: Math.min(30_000, deadline - Date.now()),
          maxBuffer: 8192,
        });
        expect(
          /^HTTP (200|201|202); scoped request completed; private response saved\n$/.test(
            result.stdout,
          ),
        ).toBe(true);
      } catch {
        // Never stringify subprocess errors: response files can contain source credentials.
        throw new Error(`Machine helper request ${calls} failed`);
      }
      expect((await stat(output)).mode & 0o777).toBe(0o600);
      return JSON.parse(await readFile(output, 'utf8')) as T;
    }
    const identity = await helper<{
      principal_id: string;
      credential_id: string;
      project_id: string;
      environment: string;
    }>();
    expect(identity.principal_id).toBe(issued.principal.id);
    expect(identity.credential_id).toBe(issued.credential.id);
    expect(identity.project_id).toBe(project.id);
    expect(identity.environment).toBe('development');
    await helper(base);
    const activation = await helper<{ active_release_revision: number }>(
      `${base}/release-activations?environment=development`,
    );
    const catalog = await helper<{ items: unknown[]; next_cursor: string | null }>(
      `${base}/analysis-catalog?environment=development&limit=200`,
    );
    expect(activation.active_release_revision).toBe(0);
    expect(catalog.items).toEqual([]);
    expect(catalog.next_cursor).toBeNull();
    const definition = {
      display_name: 'Machine SDK action',
      trigger: 'A synthetic website completes a save action.',
      allowed_source_kinds: ['browser'],
      fact_kind: 'client_observed',
      schema: {
        type: 'object',
        additionalProperties: false,
        properties: { marker: { type: 'string', minLength: 1, maxLength: 128 } },
        required: ['marker'],
      },
      example_properties: { marker: 'synthetic-machine' },
    };
    const manifest = join(privateDirectory, 'desired.json');
    const active = join(privateDirectory, 'active.json');
    await writeFile(
      manifest,
      JSON.stringify({
        format: 'activity.desired-events.v1',
        project_id: project.id,
        environment: 'development',
        complete_release: true,
        removed_event_keys: [],
        events: [
          {
            event_key: 'machine_action',
            ...definition,
            implementation_sites: [
              {
                path: 'tests/browser/machine-integration-workflow.ts',
                reason: 'Synthetic website save callback exercised by the compiled SDK.',
              },
            ],
          },
        ],
      }),
      { mode: 0o600 },
    );
    await writeFile(
      active,
      JSON.stringify({
        project_id: project.id,
        environment: 'development',
        active_release_revision: activation.active_release_revision,
        items: catalog.items,
      }),
      { mode: 0o600 },
    );
    const structural = await execute(
      'python3',
      ['integrations/tooling/activity_manifest.py', manifest],
      { timeout: 5000, maxBuffer: 8192 },
    );
    expect(JSON.parse(structural.stdout)).toEqual({
      structurally_valid: true,
      events: 1,
      server_validation_required: true,
    });
    const plan = await execute(
      'python3',
      ['integrations/tooling/activity_manifest.py', manifest, '--active-snapshot', active],
      { timeout: 5000, maxBuffer: 8192 },
    );
    expect(JSON.parse(plan.stdout)).toEqual({
      active_release_revision: 0,
      added: ['machine_action'],
      retained_requires_definition_comparison: [],
      removed: [],
      complete_release: true,
    });
    await helper(`${base}/event-definitions`, 'POST', {
      event_key: 'machine_action',
      display_name: definition.display_name,
    });
    const initial = await helper<Draft>(`${base}/event-definitions/machine_action/draft`);
    const draft = await helper<Draft>(
      `${base}/event-definitions/machine_action/draft`,
      'PATCH',
      definition,
      initial.revision,
    );
    const validated = await helper<{ valid: boolean }>(
      `${base}/event-definitions/machine_action/validate`,
      'POST',
      { draft_revision: draft.revision, sample_properties: definition.example_properties },
    );
    expect(validated.valid).toBe(true);
    const published = await helper<Release>(
      `${base}/releases`,
      'POST',
      {
        environment: 'development',
        expected_active_revision: 0,
        events: [{ event_key: 'machine_action', draft_revision: draft.revision }],
      },
      undefined,
      `release-${stamp}`,
    );
    let ready = published;
    for (let attempt = 0; !ready.ingestion_ready && attempt < 20; attempt++) {
      await delay(500);
      ready = await helper<Release>(`${base}/releases/${published.release_id}`);
    }
    expect(ready.state).toBe('published');
    expect(ready.ingestion_ready, 'real Kafka/ClickHouse readiness probe').toBe(true);
    const activated = await helper<{ active_release_revision: number }>(
      `${base}/release-activations`,
      'POST',
      { environment: 'development', release_id: published.release_id, expected_active_revision: 0 },
      undefined,
      `activate-${stamp}`,
    );
    expect(activated.active_release_revision).toBe(published.revision);
    const version = await helper<{ schema_version: number }>(
      `${base}/event-definitions/machine_action/versions/1`,
    );
    expect(version.schema_version).toBe(1);

    // Small allowlisted site serves the actual compiled SDK in a cookie-free context.
    const modules = new Map(
      await Promise.all(
        ['index.js', 'client.js', 'transport.js', 'validation.js', 'types.js'].map(
          async (name) =>
            [name, await readFile(resolve('packages/browser-sdk/dist', name))] as const,
        ),
      ),
    );
    const server = createServer((request, response) => {
      response.setHeader('Cache-Control', 'no-store');
      if (request.method !== 'GET') response.writeHead(405).end();
      else if (request.url === '/')
        response
          .writeHead(200, { 'Content-Type': 'text/html;charset=utf-8' })
          .end(
            '<!doctype html><title>Machine SDK synthetic website</title><body>Machine SDK synthetic website</body>',
          );
      else {
        const module = modules.get(request.url?.slice(1) ?? '');
        if (!module) response.writeHead(404).end();
        else
          response
            .writeHead(200, { 'Content-Type': 'application/javascript;charset=utf-8' })
            .end(module);
      }
    });
    await new Promise<void>((ready, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', () => {
        server.off('error', reject);
        ready();
      });
    });
    closeSite = () =>
      new Promise<void>((closed, reject) => {
        server.close((error) => (error ? reject(error) : closed()));
        server.closeAllConnections();
      });
    const address = server.address();
    if (!address || typeof address === 'string') throw new Error('SDK site port unavailable');
    const origin = `http://127.0.0.1:${address.port}`;
    const source = await helper<Source>(`${base}/sources`, 'POST', {
      name: `Machine SDK ${stamp}`,
      environment: 'development',
      kind: 'browser',
      allowed_origins: [origin],
    });
    const readSource = await helper<Source>(`${base}/sources/${source.id}`);
    expect(readSource.allowed_origins).toEqual([origin]);
    context = await browser.newContext({ serviceWorkers: 'block' });
    expect(await context.cookies()).toEqual([]);
    const sdkPage = await context.newPage();
    const errors: string[] = [];
    sdkPage.on('pageerror', (error) => errors.push(error.message));
    const sent: Envelope[] = [];
    await sdkPage.route(`${endpoint}/v1/collect`, async (route) => {
      const request = route.request();
      const headers = await request.allHeaders();
      expect(headers.origin).toBe(origin);
      expect('authorization' in headers).toBe(false);
      expect('cookie' in headers).toBe(false);
      const body = request.postDataJSON() as { events: Envelope[] };
      sent.push(...body.events);
      await route.continue();
    });
    await sdkPage.goto(origin);
    const started = Date.now();
    const uid = `machine-user-${stamp}`;
    const marker = `machine-${stamp}`;
    const emitted = await sdkPage.evaluate(
      async ({ moduleURL, endpoint, projectID, writeKey, uid, marker }) => {
        const { createEviaClient } = await import(moduleURL);
        const client: BrowserClient = createEviaClient({
          endpoint,
          projectId: projectID,
          environment: 'development',
          writeKey,
          storage: null,
          lifecycle: false,
          flushIntervalMs: 1000,
          requestTimeoutMs: 5000,
          maxAttempts: 2,
        });
        (window as SDKWindow).machineIntegrationClient = client;
        await client.init();
        client.identify(uid);
        const navigation = client.navigate({
          routeName: 'Machine Save',
          navigationType: 'initial',
        });
        const emission = client.emit('machine_action', { marker });
        await client.flush();
        const outcome = await emission.result;
        return {
          id: emission.eventId,
          status: outcome.status,
          receipt: !!outcome.receiptId,
          pending: client.pending,
          pageViewID: navigation.page_view_id,
        };
      },
      {
        moduleURL: `${origin}/index.js`,
        endpoint,
        projectID: project.id,
        writeKey: source.write_key,
        uid,
        marker,
      },
    );
    expect(emitted.status).toBe('accepted');
    expect(emitted.receipt).toBe(true);
    expect(emitted.pending).toBe(0);
    expect(sent).toHaveLength(1);
    expect(sent[0]!.event_id).toBe(emitted.id);
    const request = {
      kind: 'events',
      environment: 'development',
      timezone: 'UTC',
      time_range: {
        from: new Date(started - 5000).toISOString(),
        to: new Date(Date.now() + 5000).toISOString(),
      },
      event_versions: [{ event_key: 'machine_action', schema_version: 1 }],
      filter: {
        op: 'eq',
        field: { kind: 'property', event_key: 'machine_action', schema_version: 1, key: 'marker' },
        value: marker,
      },
      limit: 10,
    };
    await helper(`${base}/query-validations`, 'POST', request);
    let rows: QueryResult['rows'] = [];
    for (let attempt = 0; attempt < 4; attempt++) {
      const job = await helper<QueryJob>(`${base}/queries`, 'POST', request);
      let current = job;
      for (let poll = 0; ['queued', 'running'].includes(current.state) && poll < 25; poll++) {
        await delay(200);
        current = await helper<QueryJob>(`${base}/queries/${job.query_id}`);
      }
      expect(current.state, `machine query ${attempt + 1} completes`).toBe('succeeded');
      const result = await helper<QueryResult>(`${base}/queries/${job.query_id}/result`);
      rows = result.rows;
      expect(rows.length).toBeLessThanOrEqual(1);
      if (rows.length === 1) break;
      await delay(500);
    }
    expect(rows).toHaveLength(1);
    const stored = rows[0]!;
    expect(stored.event_id).toBe(emitted.id);
    expect(stored.source_id).toBe(source.id);
    expect(stored.uid).toBe(uid);
    expect(stored.anonymous_id).toBe(sent[0]!.identity.anonymous_id);
    expect(stored.context).toEqual(sent[0]!.context);
    expect(stored.properties).toEqual({ marker });
    expect(sent[0]!.context.page_view_id).toBe(emitted.pageViewID);
    expect(await context.cookies()).toEqual([]);
    expect(errors).toEqual([]);
    await human(() =>
      api.revokeMachineCredential(
        project.id,
        issued.principal.id,
        issued.credential.id,
        issued.principal.revision,
        session.csrf_token,
      ),
    );
    let revoked = false;
    const revokedOutput = join(privateDirectory, 'revoked.json');
    try {
      await execute(
        'python3',
        ['integrations/tooling/activity_api.py', ...scope, '--output', revokedOutput],
        { env: { ...process.env, EVIA_MACHINE_TOKEN: token }, timeout: 25_000, maxBuffer: 8192 },
      );
    } catch (error) {
      const result = error as { code?: number; stdout?: string; stderr?: string };
      revoked =
        result.code === 1 &&
        result.stdout === '' &&
        result.stderr === 'Request failed (HTTP 401); no response body printed.\n';
    }
    expect(revoked, 'revoked helper bearer fails401 without owner cookie fallback').toBe(true);
    expect(
      await stat(revokedOutput).then(
        () => true,
        () => false,
      ),
    ).toBe(false);
  } finally {
    if (context) {
      for (const current of context.pages())
        await current
          .evaluate(() => (window as SDKWindow).machineIntegrationClient?.destroy())
          .catch(() => {});
      await context.close();
    }
    if (closeSite) await closeSite();
    await rm(privateDirectory, { recursive: true, force: true });
  }
}
