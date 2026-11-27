import { randomUUID } from 'node:crypto';
import { readdir, readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { expect, test, type BrowserContext, type Page } from '@playwright/test';
import { loginAsE2EUser } from './auth-session';

// Credential responses must never enter automatic trace or screenshot artifacts.
test.use({ trace: 'off', screenshot: 'off', video: 'off', reducedMotion: 'reduce' });

const username = process.env.EVIA_E2E_USERNAME;
const password = process.env.EVIA_E2E_PASSWORD;

test('the identity workspace lists folded revisions and resolves a member', async ({ page }) => {
  test.skip(
    process.env.EVIA_E2E_FULL_STACK !== '1' || !username || !password,
    'Requires EVIA_E2E_FULL_STACK=1 with serve, ingest/query/identity workers and real dependencies.',
  );
  test.setTimeout(300_000);
  page.setDefaultTimeout(15_000);
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));

  await loginAsE2EUser(page, username, password);
  const sessionResponse = await page.request.get('/api/v1/session');
  const { csrf_token: csrf } = (await sessionResponse.json()) as { csrf_token: string };
  const headers = { Origin: new URL(page.url()).origin, 'X-CSRF-Token': csrf };
  const stamp = randomUUID();

  const created = await page.request.post('/api/v1/projects', {
    headers,
    data: { name: `Identity workspace ${stamp}`, timezone: 'UTC' },
  });
  expect(created.status()).toBe(201);
  const project = (await created.json()) as { id: string };
  const base = `/api/v1/projects/${project.id}`;
  const definition = {
    display_name: '身份工作区操作',
    trigger: '合成站点的演示按钮操作。',
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
  await page.request.post(`${base}/event-definitions`, {
    headers,
    data: { event_key: 'browser_action', display_name: definition.display_name },
  });
  const initial = (await (
    await page.request.get(`${base}/event-definitions/browser_action/draft`, { headers })
  ).json()) as { revision: number };
  const draft = (await (
    await page.request.patch(`${base}/event-definitions/browser_action/draft`, {
      headers: { ...headers, 'If-Match': `"${initial.revision}"` },
      data: definition,
    })
  ).json()) as { revision: number };
  const release = (await (
    await page.request.post(`${base}/releases`, {
      headers: { ...headers, 'Idempotency-Key': `identity-ui-${stamp}` },
      data: {
        environment: 'development',
        expected_active_revision: 0,
        events: [{ event_key: 'browser_action', draft_revision: draft.revision }],
      },
    })
  ).json()) as { release_id: string; ingestion_ready: boolean };
  for (let attempt = 0; !release.ingestion_ready && attempt < 20; attempt++) {
    await delay(500);
    const polled = (await (
      await page.request.get(`${base}/releases/${release.release_id}`, { headers })
    ).json()) as { ingestion_ready: boolean };
    release.ingestion_ready = polled.ingestion_ready;
  }
  expect(release.ingestion_ready).toBe(true);
  const activated = await page.request.post(`${base}/release-activations`, {
    headers: { ...headers, 'Idempotency-Key': `identity-ui-activate-${stamp}` },
    data: {
      environment: 'development',
      release_id: release.release_id,
      expected_active_revision: 0,
    },
  });
  expect(activated.status()).toBe(200);

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
      response.end('<!doctype html><title>identity site</title><body>identity site</body>');
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
    const origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
    const source = (await (
      await page.request.post(`${base}/sources`, {
        headers,
        data: {
          name: `Identity workspace ${stamp}`,
          environment: 'development',
          kind: 'browser',
          allowed_origins: [origin],
        },
      })
    ).json()) as { write_key: string };
    context = await page.context().browser()!.newContext({ serviceWorkers: 'block' });
    const sdkPage = await context.newPage();
    const uid = `identity-ui-${stamp.slice(0, 8)}`;
    const deviceA = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
    const deviceB = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
    const seed = `evia:${project.id}:development:${source.write_key}:anonymous`;
    async function emitAs(device: string, account: string) {
      await sdkPage.goto(origin);
      const outcome = await sdkPage.evaluate(
        async ({ moduleURL, options, uid: account }) => {
          localStorage.setItem(options.seed, options.anonymous);
          const { createEviaClient } = await import(moduleURL);
          const client = createEviaClient({
            endpoint: options.endpoint,
            projectId: options.projectId,
            environment: 'development',
            writeKey: options.writeKey,
            autoIdentityLink: true,
            lifecycle: false,
            flushIntervalMs: 300,
          });
          await client.init();
          client.identify(account);
          const emission = client.emit('browser_action', { page: options.marker });
          await client.flush();
          return (await emission.result).status;
        },
        {
          moduleURL: `${origin}/index.js`,
          options: {
            endpoint: process.env.EVIA_E2E_API ?? 'http://127.0.0.1:8080',
            projectId: project.id,
            writeKey: source.write_key,
            marker: `identity-ui-${stamp.slice(0, 8)}`,
            anonymous: device,
            seed,
          },
          uid: account,
        },
      );
      expect(outcome).toBe('accepted');
    }
    await emitAs(deviceA, uid);
    await emitAs(deviceB, uid);

    // Wait for the identity worker to fold a completed revision through the
    // real API the workspace consumes.
    const deadline = Date.now() + 150_000;
    let folded = false;
    while (Date.now() < deadline) {
      await delay(5000);
      const response = await page.request.get(
        `${base}/identity-revisions?environment=development`,
        { headers },
      );
      if (response.status() === 200) {
        const body = (await response.json()) as { items: { state: string }[] };
        if (body.items.some((item) => item.state === 'completed')) {
          folded = true;
          break;
        }
      }
    }
    expect(folded, 'identity worker folded a revision').toBe(true);

    await page.goto(`/projects/${project.id}/identity?environment=development`);
    await expect(page.getByRole('heading', { name: '身份修订', exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: '身份修订', exact: true })).toBeVisible();
    await expect(page.locator('section[aria-label="修订历史"]')).toBeVisible();
    const table = page.locator('section[aria-label="查询结果表"], [aria-label="身份修订列表"]');
    await expect
      .poll(async () => page.locator('table').count(), { timeout: 15_000 })
      .toBeGreaterThan(0);
    await expect(page.getByText('已完成').first()).toBeVisible();

    // Resolve the account across both devices through the workspace form.
    await page.getByLabel('用户标识 uid', { exact: false }).fill(uid);
    await page.getByRole('button', { name: '解析', exact: true }).click();
    await expect(page.locator('section[aria-label="解析身份"] table')).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByText('解析结果：resolved person', { exact: false })).toBeVisible();
    await expect(page.getByTitle(deviceA).first()).toBeVisible();
    await expect(page.getByTitle(uid).first()).toBeVisible();

    // Unknown identities surface the not-in-graph error state.
    await page.getByLabel('用户标识 uid', { exact: false }).fill('no-such-uid');
    await page.getByRole('button', { name: '解析', exact: true }).click();
    await expect(page.getByText('该身份尚未进入身份图', { exact: false })).toBeVisible({
      timeout: 15_000,
    });

    expect(errors).toEqual([]);
  } finally {
    await context?.close();
    await new Promise<void>((done) => server.close(() => done()));
  }
});
