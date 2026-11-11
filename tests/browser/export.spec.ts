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

async function openExports(page: Page, projectID: string) {
  await page.goto(`/projects/${projectID}/exports?environment=development`);
  await expect(page.getByRole('heading', { name: '数据导出' })).toBeVisible();
  await expect(page.locator('section[aria-label="发起数据导出"]')).toBeVisible();
}

function exportJobFixture(projectID: string, jobID: string, state: string) {
  const now = new Date().toISOString();
  return {
    job_id: jobID,
    project_id: projectID,
    environment: 'development',
    name: '待取消的导出',
    format: 'json',
    state,
    error_code: null,
    result_rows: null,
    artifact_bytes: null,
    artifact_sha256: null,
    expires_at: null,
    created_at: now,
    updated_at: now,
    finished_at: state === 'queued' ? null : now,
  };
}

test('owner creates an export and downloads a completed artifact', async ({ page }, testInfo) => {
  await login(page);
  const projectID = await createProject(page, `Export workspace ${Date.now()}`);
  await openExports(page, projectID);

  // The owner-only workspace is reachable from the project navigation.
  await expect(page.getByRole('link', { name: '数据导出' })).toBeVisible();

  const form = page.locator('section[aria-label="发起数据导出"]');
  // Client-side validation blocks an empty name before any write.
  await page.getByRole('button', { name: '发起导出' }).click();
  await expect(page.getByRole('alert').filter({ hasText: '请填写导出名称' })).toBeVisible();

  await form.getByLabel('导出名称').fill('每周行为趋势导出');
  await form.getByRole('combobox', { name: '查询类型' }).selectOption('trend');
  await form.getByLabel(/时间窗口/).fill('3');
  await form.getByLabel(/结果行数上限/).fill('100');

  // A valid request opens the confirmation modal first.
  await page.getByRole('button', { name: '发起导出' }).click();
  const dialog = page.getByRole('dialog', { name: '确认发起数据导出' });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('每周行为趋势导出');

  const created = page.waitForResponse(
    (response) => response.url().endsWith('/exports') && response.request().method() === 'POST',
  );
  await dialog.getByRole('button', { name: '确认导出' }).click();
  expect((await created).status()).toBe(201);
  await expect(dialog).not.toBeVisible();

  // The export-worker materializes the JSON artifact and the job completes.
  const list = page.locator('section[aria-label="导出作业"]');
  await expect(list.getByRole('table')).toBeVisible();
  await expect(list.getByText('已完成')).toBeVisible({ timeout: 30_000 });
  await expect(list.getByRole('button', { name: '申请下载' })).toBeVisible();

  // The one-time grant immediately redeems into a same-origin attachment download.
  const downloadRequest = page.waitForRequest((request) =>
    request.url().includes('/api/v1/exports/downloads/'),
  );
  const downloadEvent = page.waitForEvent('download');
  await list.getByRole('button', { name: '申请下载' }).click();
  const request = await downloadRequest;
  expect(request.method()).toBe('GET');
  // The capability URL carries no session cookie requirement; the token is in
  // the path and is single-use.
  expect(new URL(request.url()).pathname).toMatch(/^\/api\/v1\/exports\/downloads\/[a-f0-9]{32,}$/);
  const download = await downloadEvent;
  expect(download.suggestedFilename()).toMatch(/^[0-9a-fA-F-]{36}\.json$/);

  await page.screenshot({
    path: testInfo.outputPath('desktop-export-workspace.png'),
    fullPage: true,
  });

  // Mobile layout keeps the form and table usable.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(form.getByLabel('导出名称')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('mobile-export.png'), fullPage: true });
});

test('owner cancels a queued export job from the workspace table', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Export cancel ${Date.now()}`);
  const jobID = crypto.randomUUID();

  // The live export-worker drains queued jobs within its 250ms poll interval,
  // so the queued-only cancel path is exercised deterministically with route
  // mocks while login, session, routing and CSRF wiring stay fully real.
  let cancelled = false;
  await page.route(`**/api/v1/projects/${projectID}/exports?*`, async (route) => {
    await route.fulfill({
      json: {
        items: [exportJobFixture(projectID, jobID, cancelled ? 'cancelled' : 'queued')],
        next_cursor: null,
      },
    });
  });
  await page.route(`**/api/v1/projects/${projectID}/exports/${jobID}`, async (route) => {
    cancelled = true;
    await route.fulfill({ json: exportJobFixture(projectID, jobID, 'cancelled') });
  });

  await openExports(page, projectID);
  const list = page.locator('section[aria-label="导出作业"]');
  await expect(list.getByRole('button', { name: '取消' })).toBeVisible();

  const cancelRequest = page.waitForRequest(
    (request) => request.method() === 'DELETE' && request.url().includes(`/exports/${jobID}`),
  );
  await list.getByRole('button', { name: '取消' }).click();
  const request = await cancelRequest;
  expect(request.headers()['x-csrf-token'], 'cancel must carry the CSRF token').toBeTruthy();

  // The refetched table reflects the terminal cancelled state and hides the action.
  await expect(list.getByText('已取消')).toBeVisible();
  await expect(list.getByRole('button', { name: '取消' })).toHaveCount(0);
});
