import { expect, test, type Page } from '@playwright/test';
import type { Diagnostics } from '../../packages/api-client/src/index';

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
  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('账号', { exact: false }).fill(username!);
  await page.getByLabel('密码', { exact: false }).fill(password!);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
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

function diagnosticsFixture(): Diagnostics {
  return {
    project_id: '11111111-1111-4111-8111-111111111111',
    generated_at: new Date().toISOString(),
    features: {
      collection_enabled: true,
      analysis_enabled: true,
      delivery_enabled: false,
    },
    sources: {
      status: 'ok',
      total: 2,
      truncated: false,
      by_status: [
        { name: 'active', count: 1 },
        { name: 'disabled', count: 1 },
      ],
      by_environment: [{ name: 'production', count: 2 }],
      disabled_sources: [
        {
          id: '44444444-4444-4444-8444-444444444444',
          name: 'legacy-web',
          environment: 'production',
          kind: 'browser',
        },
      ],
    },
    budget: {
      status: 'ok',
      rate_per_second: 1000,
      burst: 2000,
      environments: [
        {
          environment: 'production',
          rate_per_second: 5,
          burst: 60,
          override: true,
        },
      ],
    },
    pipeline: {
      status: 'ok',
      proofs: [
        {
          environment: 'production',
          release_id: '55555555-5555-4555-8555-555555555555',
          revision: 3,
          observed_at: '2026-11-12T09:59:00.000Z',
          age_seconds: 60,
          proof_count: 2,
        },
      ],
    },
    rejections: {
      status: 'ok',
      window_hours: 24,
      totals: [{ reason: 'INVALID_EVENT', count: 7 }],
      windows: [
        {
          bucket_at: '2026-11-12T09:00:00.000Z',
          environment: 'production',
          reasons: [{ reason: 'INVALID_EVENT', count: 7 }],
        },
      ],
    },
  };
}

test('member reads the collection diagnostics workspace', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Diagnostics ${Date.now()}`);

  await page.route(`**/api/v1/projects/${projectID}/diagnostics`, async (route) => {
    await route.fulfill({ json: diagnosticsFixture() });
  });

  await page.goto(`/projects/${projectID}/diagnostics?environment=development`);
  await expect(page.getByRole('heading', { name: '采集诊断' })).toBeVisible();
  await expect(page.getByRole('link', { name: '采集诊断' })).toBeVisible();

  await expect(page.getByText('实时采集')).toBeVisible();
  await expect(page.getByText('已启用').first()).toBeVisible();

  const sources = page.locator('section[aria-label="接入来源"]');
  await expect(sources.getByText('正常')).toBeVisible();
  await expect(sources.getByText('legacy-web')).toBeVisible();

  const budget = page.locator('section[aria-label="采集预算"]');
  await expect(budget.getByText('项目覆盖')).toBeVisible();
  await expect(budget.getByText('2000')).toBeVisible();

  const pipeline = page.locator('section[aria-label="管道就绪探针"]');
  await expect(pipeline.getByText('1 分钟')).toBeVisible();

  const rejections = page.locator('section[aria-label="接收拒绝统计（近 24 小时）"]');
  await expect(rejections.getByText('INVALID_EVENT × 7')).toBeVisible();
  await expect(rejections.getByText('拒绝事件总数 7')).toBeVisible();
});

test('diagnostics workspace surfaces degraded sections', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Diagnostics Degraded ${Date.now()}`);

  const degraded = diagnosticsFixture();
  degraded.budget = {
    status: 'unavailable',
    error_code: 'DIAGNOSTICS_BUDGET_UNAVAILABLE',
    rate_per_second: 0,
    burst: 0,
    environments: [],
  };
  degraded.pipeline = { status: 'no_data', proofs: [] };
  await page.route(`**/api/v1/projects/${projectID}/diagnostics`, async (route) => {
    await route.fulfill({ json: degraded });
  });

  await page.goto(`/projects/${projectID}/diagnostics?environment=development`);
  await expect(page.getByRole('heading', { name: '采集诊断' })).toBeVisible();
  const budget = page.locator('section[aria-label="采集预算"]');
  await expect(budget.getByText('不可用')).toBeVisible();
  await expect(budget.getByText('DIAGNOSTICS_BUDGET_UNAVAILABLE')).toBeVisible();
  const pipeline = page.locator('section[aria-label="管道就绪探针"]');
  await expect(pipeline.getByText('暂无数据')).toBeVisible();
});
