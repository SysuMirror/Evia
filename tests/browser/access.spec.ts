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
  await page.getByLabel('账号', { exact: true }).fill(username!);
  await page.getByLabel('密码', { exact: true }).fill(password!);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
}

test('recover from failed login and project requests, persist details and revoke the session', async ({
  page,
}, testInfo) => {
  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('账号', { exact: true }).fill(username!);
  await page.getByLabel('密码', { exact: true }).fill(`${password!}-invalid`);
  const failedLogin = page.waitForResponse('/api/v1/session/login');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  expect((await failedLogin).status()).toBe(401);
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page.getByLabel('账号', { exact: true })).toHaveValue(username!);
  await expect(page.getByLabel('密码', { exact: true })).toHaveValue(`${password!}-invalid`);
  await page.getByLabel('密码', { exact: true }).fill(password!);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
  await page.getByRole('button', { name: '创建项目', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '创建项目' });
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(dialog.getByText('请输入项目名称', { exact: true })).toBeVisible();
  const name = `Browser project ${Date.now()}`;
  await dialog.getByLabel('项目名称', { exact: true }).fill(name);
  await page.route('**/api/v1/projects', (route) => route.abort('failed'));
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('无法连接服务');
  await expect(dialog.getByLabel('项目名称', { exact: true })).toHaveValue(name);
  await expect(dialog.getByRole('combobox', { name: '统计时区' })).toBeEnabled();
  await expect(dialog.getByText('Asia/Shanghai', { exact: true })).toBeVisible();
  await page.unroute('**/api/v1/projects');
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole('button', { name, exact: true }).click();
  await expect(
    page.getByRole('dialog', { name: '项目详情' }).getByText('Asia/Shanghai'),
  ).toBeVisible();
  await page
    .getByRole('dialog', { name: '项目详情' })
    .getByRole('button', { name: '完成', exact: true })
    .click();
  await page.reload();
  await expect(page.getByRole('button', { name, exact: true })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('desktop-projects.png'), fullPage: true });
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page).toHaveURL(/\/login$/);
  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  expect((await page.request.get('/api/v1/session')).status()).toBe(401);
});

test('real API rejects missing CSRF and foreign origins without creating projects', async ({
  page,
}) => {
  await login(page);
  const before = await (await page.request.get('/api/v1/projects?limit=100')).json();
  const denied = await page.request.post('/api/v1/projects', {
    data: { name: 'Must not exist', timezone: 'UTC' },
  });
  expect(denied.status()).toBe(403);
  expect((await denied.json()).code).toBe('CSRF_REQUIRED');
  const session = await (await page.request.get('/api/v1/session')).json();
  const foreign = await page.request.post('/api/v1/projects', {
    headers: { Origin: 'https://foreign.example', 'X-CSRF-Token': session.csrf_token },
    data: { name: 'Must not exist', timezone: 'UTC' },
  });
  expect(foreign.status()).toBe(403);
  expect((await foreign.json()).code).toBe('ORIGIN_FORBIDDEN');
  const after = await (await page.request.get('/api/v1/projects?limit=100')).json();
  expect(after).toEqual(before);
});

test('preserve unsaved input and support narrow layouts and keyboard focus', async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  await page.screenshot({ path: testInfo.outputPath('mobile-projects.png'), fullPage: true });
  await page.getByRole('button', { name: '创建项目', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '创建项目' });
  await expect(dialog.getByLabel('项目名称', { exact: true })).toBeFocused();
  await dialog.getByLabel('项目名称', { exact: true }).fill('Unsaved mobile project');
  await page.screenshot({ path: testInfo.outputPath('mobile-create.png'), fullPage: true });
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('combobox', { name: '统计时区' })).toBeFocused();
  await dialog.getByRole('button', { name: '取消', exact: true }).click();
  const confirmation = page.getByRole('dialog', { name: '放弃尚未保存的项目？' });
  await expect(confirmation.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await confirmation.getByRole('button', { name: '继续编辑' }).click();
  await expect(confirmation).not.toBeVisible();
  await expect(dialog.getByLabel('项目名称', { exact: true })).toBeFocused();
  await expect(dialog.getByLabel('项目名称', { exact: true })).toHaveValue(
    'Unsaved mobile project',
  );
  await page.keyboard.press('Escape');
  await expect(confirmation.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(confirmation).not.toBeVisible();
  await expect(dialog.getByLabel('项目名称', { exact: true })).toBeFocused();
  await expect(dialog.getByLabel('项目名称', { exact: true })).toHaveValue(
    'Unsaved mobile project',
  );
  await page.keyboard.press('Escape');
  await confirmation.getByRole('button', { name: '放弃', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole('button', { name: '创建项目', exact: true })).toBeFocused();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});
