import { expect, test, type Page } from '@playwright/test';

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

async function sessionHeaders(page: Page) {
  const session = await (await page.request.get('/api/v1/session')).json();
  return {
    Origin: 'http://127.0.0.1:5173',
    'X-CSRF-Token': session.csrf_token as string,
  };
}

test('owner configures per-environment retention policy with optimistic revision control', async ({
  page,
}, testInfo) => {
  await login(page);
  const projectID = await createProject(page, `Retention workspace ${Date.now()}`);
  await page.goto(`/projects/${projectID}/retention?environment=development`);

  await expect(page.getByRole('heading', { name: '数据保留' })).toBeVisible();
  await expect(page.getByRole('link', { name: '数据保留' })).toBeVisible();
  const days = page.locator('#retention-days');
  const envSelect = page.locator('#retention-environment');

  // Unconfigured scope: blank editor and revision zero.
  await expect(days).toBeEmpty();
  await expect(page.getByText(/当前修订版本：0/)).toBeVisible();

  // Client-side bounds validation never leaves the browser.
  await days.fill('29');
  await page.getByRole('button', { name: '创建保留策略' }).click();
  await expect(page.getByText(/保留天数必须在 30 到 3650 天之间/)).toBeVisible();

  // Create the first policy with the expect-absent zero revision.
  await days.fill('90');
  await page.getByRole('button', { name: '创建保留策略' }).click();
  await expect(page.getByRole('button', { name: '更新保留策略' })).toBeVisible();
  await expect(page.getByText(/当前修订版本：1/)).toBeVisible();
  await expect(page.getByText(/保留最近 90 天/)).toBeVisible();

  // A second environment is an independent, still-unconfigured scope.
  await envSelect.selectOption('staging');
  await expect(days).toBeEmpty();
  await expect(page.getByText(/当前修订版本：0/)).toBeVisible();

  // Switching back restores the persisted development policy.
  await envSelect.selectOption('development');
  await expect(days).toHaveValue('90');
  await expect(page.getByText(/当前修订版本：1/)).toBeVisible();

  // Update advances the revision.
  await days.fill('180');
  await page.getByRole('button', { name: '更新保留策略' }).click();
  await expect(page.getByText(/当前修订版本：2/)).toBeVisible();

  // Stale If-Match revision is rejected as a conflict at the API boundary.
  const headers = await sessionHeaders(page);
  const conflict = await page.request.put(`/api/v1/projects/${projectID}/retention-policy`, {
    headers: { ...headers, 'If-Match': '"1"', 'Idempotency-Key': crypto.randomUUID() },
    data: { environment: 'development', retention_days: 365 },
  });
  expect(conflict.status()).toBe(409);
  const problem = await conflict.json();
  expect(['RETENTION_REVISION_CONFLICT', 'IDEMPOTENCY_CONFLICT']).toContain(problem.code);

  // A missing required environment query is a 400, not a 500.
  const missing = await page.request.get(`/api/v1/projects/${projectID}/retention-policy`);
  expect(missing.status()).toBe(400);

  // The sweep history read path returns a bounded list for the owner.
  const sweeps = await page.request.get(
    `/api/v1/projects/${projectID}/retention-sweeps?environment=development&limit=20`,
  );
  expect(sweeps.status()).toBe(200);
  const sweepBody = await sweeps.json();
  expect(Array.isArray(sweepBody.items)).toBe(true);
  // Invalid limit is rejected at the transport boundary.
  const badLimit = await page.request.get(
    `/api/v1/projects/${projectID}/retention-sweeps?limit=101`,
  );
  expect(badLimit.status()).toBe(400);

  // The workspace renders the cleanup history surface for the selected scope.
  await expect(page.locator('section[aria-label="清理历史"]')).toBeVisible();

  if (testInfo.errors.length === 0) {
    await page.screenshot({
      path: testInfo.outputPath('retention-policy-desktop.png'),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: testInfo.outputPath('retention-policy-mobile.png'),
      fullPage: true,
    });
  }
});
