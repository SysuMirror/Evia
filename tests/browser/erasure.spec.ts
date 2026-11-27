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

async function openErasure(page: Page, projectID: string) {
  await page.goto(`/projects/${projectID}/erasure?environment=development`);
  await expect(page.getByRole('heading', { name: '数据删除', exact: true })).toBeVisible();
  await expect(page.locator('section[aria-label="发起数据删除"]')).toBeVisible();
}

test('owner creates an erasure job and watches it reach a terminal state', async ({
  page,
}, testInfo) => {
  await login(page);
  const projectID = await createProject(page, `Erasure workspace ${Date.now()}`);
  await openErasure(page, projectID);

  // The owner-only workspace is reachable from the project navigation.
  await expect(page.getByRole('link', { name: '数据删除' })).toBeVisible();

  const form = page.locator('section[aria-label="发起数据删除"]');
  // Client-side validation blocks obvious mistakes before any write.
  await page.getByRole('button', { name: '发起删除' }).click();
  await expect(page.getByRole('alert').filter({ hasText: '请填写要删除的身份标识' })).toBeVisible();

  await form.getByRole('combobox', { name: '身份类型' }).selectOption('anonymous_id');
  await form.getByLabel(/匿名标识/).fill('not-a-uuid');
  await form.getByLabel('删除原因（审计必填）').fill('被遗忘权请求');
  await page.getByRole('button', { name: '发起删除' }).click();
  await expect(page.getByRole('alert').filter({ hasText: '匿名标识必须是 UUID' })).toBeVisible();

  // A valid uid request opens the destructive confirmation modal first.
  const uid = `erased-user-${Date.now()}`;
  await form.getByRole('combobox', { name: '身份类型' }).selectOption('uid');
  await form.getByLabel('用户标识 uid').fill(uid);
  await page.getByRole('button', { name: '发起删除' }).click();
  const dialog = page.getByRole('dialog', { name: '确认发起数据删除' });
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('code')).toContainText(uid);

  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith('/erasure-jobs') && response.request().method() === 'POST',
  );
  await dialog.getByRole('button', { name: '确认删除' }).click();
  expect((await created).status()).toBe(201);
  await expect(dialog).not.toBeVisible();

  // The job is listed immediately and the worker drives it to completion.
  // The state Badge renders as plain text (no live-region role); follow the
  // same getByText convention used by the metrics completion assertions.
  const list = page.locator('section[aria-label="删除作业"]');
  await expect(list.getByRole('table')).toBeVisible();
  await expect(list.getByText('已完成')).toBeVisible({ timeout: 20_000 });

  await page.screenshot({ path: testInfo.outputPath('desktop-erasure-job.png'), fullPage: true });

  // Mobile layout keeps both surfaces usable.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(form.getByRole('combobox', { name: '环境' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('mobile-erasure.png'), fullPage: true });
});

test('parallel requests for the same identity allow exactly one active job', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Erasure conflict ${Date.now()}`);
  const session = await (await page.request.get('/api/v1/session')).json();
  const uid = `conflict-user-${Date.now()}`;
  const body = { environment: 'development', uid, reason: 'parallel submission test' };
  const [first, second] = await Promise.all([
    page.request.post(`/api/v1/projects/${projectID}/erasure-jobs`, {
      headers: {
        Origin: 'http://127.0.0.1:5173',
        'X-CSRF-Token': session.csrf_token,
        'Idempotency-Key': crypto.randomUUID(),
      },
      data: body,
    }),
    page.request.post(`/api/v1/projects/${projectID}/erasure-jobs`, {
      headers: {
        Origin: 'http://127.0.0.1:5173',
        'X-CSRF-Token': session.csrf_token,
        'Idempotency-Key': crypto.randomUUID(),
      },
      data: body,
    }),
  ]);
  const statuses = [first.status(), second.status()].sort();
  expect(statuses).toEqual([201, 409]);
});
