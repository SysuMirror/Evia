import { expect, test, type Page } from '@playwright/test';
import { loginAsE2EUser } from './auth-session';

const username = process.env.EVIA_E2E_USERNAME;
const password = process.env.EVIA_E2E_PASSWORD;
if (!username || !password)
  throw new Error(
    'Set EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD for the dedicated local test account.',
  );

const pageErrors = new WeakMap<Page, string[]>();
test.beforeEach(({ page }) => {
  const errors: string[] = [];
  pageErrors.set(page, errors);
  page.on('pageerror', (error) => errors.push(error.message));
});
test.afterEach(({ page }) => {
  expect(pageErrors.get(page), 'no uncaught browser exceptions').toEqual([]);
});

async function login(page: Page) {
  await loginAsE2EUser(page, username, password);
}

async function createProject(page: Page, name: string): Promise<string> {
  const session = await (await page.request.get('/api/v1/session')).json();
  const response = await page.request.post('/api/v1/projects', {
    headers: { Origin: 'http://127.0.0.1:5173', 'X-CSRF-Token': session.csrf_token },
    data: { name, timezone: 'UTC' },
  });
  expect(response.status(), `create project: ${await response.text()}`).toBe(201);
  return ((await response.json()) as { id: string }).id;
}

function quotaFixture(overrides: Partial<Record<string, unknown>> = {}) {
  return {
    project_id: '11111111-1111-4111-8111-111111111111',
    environment: 'production',
    rate_per_second: 1000,
    burst: 2000,
    override: false,
    source: 'default',
    revision: 0,
    runtime_status: 'applied',
    created_at: null,
    updated_at: null,
    ...overrides,
  };
}

function diagnosticsPayload() {
  return {
    project_id: '11111111-1111-4111-8111-111111111111',
    generated_at: new Date().toISOString(),
    features: { collection_enabled: true, analysis_enabled: true, delivery_enabled: false },
    sources: {
      status: 'ok',
      total: 0,
      truncated: false,
      by_status: [],
      by_environment: [],
      disabled_sources: [],
    },
    budget: { status: 'ok', rate_per_second: 1000, burst: 2000, environments: [] },
    pipeline: { status: 'no_data', proofs: [] },
    rejections: { status: 'ok', window_hours: 24, totals: [], windows: [] },
  };
}

test('owner reads effective quota and publishes an override', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Quotas ${Date.now()}`);

  let revision = 0;
  await page.route(`**/api/v1/projects/${projectID}/quotas*`, async (route) => {
    const request = route.request();
    if (request.method() === 'PATCH') {
      revision += 1;
      await route.fulfill({
        json: quotaFixture({
          override: true,
          source: 'project_override',
          revision,
          rate_per_second: 5,
          burst: 60,
          runtime_status: 'applied',
        }),
      });
      return;
    }
    const url = new URL(request.url());
    await route.fulfill({
      json: quotaFixture(
        revision > 0
          ? {
              override: true,
              source: 'project_override',
              revision,
              rate_per_second: 5,
              burst: 60,
            }
          : { environment: url.searchParams.get('environment') ?? 'development' },
      ),
    });
  });
  await page.route(`**/api/v1/projects/${projectID}/diagnostics`, async (route) => {
    await route.fulfill({ json: diagnosticsPayload() });
  });

  await page.goto(`/projects/${projectID}/diagnostics?environment=production`);
  const panel = page.locator('section[aria-label="采集配额管理"]');
  await expect(panel.getByText('默认策略')).toBeVisible();

  await page.locator('#quota-environment').selectOption('production');
  await expect(panel.getByText('1000 事件/秒')).toBeVisible();

  await page.locator('#quota-rate').fill('5');
  await page.locator('#quota-burst').fill('60');
  const patchRequest = page.waitForRequest(
    (request) => request.method() === 'PATCH' && request.url().includes('/quotas'),
  );
  await panel.getByRole('button', { name: '发布配额覆盖' }).click();
  const sent = await patchRequest;
  expect(sent.headers()['x-csrf-token']).toBeTruthy();
  expect(sent.headers()['if-match']).toBe('"0"');
  expect(sent.postDataJSON()).toEqual({ environment: 'production', rate_per_second: 5, burst: 60 });

  await expect(panel.getByText('项目覆盖')).toBeVisible();
  await expect(panel.getByText('5 事件/秒')).toBeVisible();
});

test('quota panel surfaces bucket reset pending warning', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Quotas Pending ${Date.now()}`);

  await page.route(`**/api/v1/projects/${projectID}/quotas*`, async (route) => {
    const request = route.request();
    if (request.method() === 'PATCH') {
      await route.fulfill({
        json: quotaFixture({
          override: true,
          source: 'project_override',
          revision: 1,
          rate_per_second: 5,
          burst: 60,
          runtime_status: 'bucket_reset_pending',
        }),
      });
      return;
    }
    await route.fulfill({ json: quotaFixture() });
  });
  await page.route(`**/api/v1/projects/${projectID}/diagnostics`, async (route) => {
    await route.fulfill({ json: diagnosticsPayload() });
  });

  await page.goto(`/projects/${projectID}/diagnostics?environment=development`);
  const panel = page.locator('section[aria-label="采集配额管理"]');
  await page.locator('#quota-rate').fill('5');
  await page.locator('#quota-burst').fill('60');
  await panel.getByRole('button', { name: '发布配额覆盖' }).click();
  await expect(panel.getByText(/Redis 桶重置失败/)).toBeVisible();
});
