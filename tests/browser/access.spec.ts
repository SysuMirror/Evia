import { expect, test, type Page, type Locator } from '@playwright/test';

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

async function expectKeyboardFocus(control: Locator) {
  await expect(control).toBeFocused();
  expect(
    await control.evaluate((element) => {
      const style = getComputedStyle(element);
      return (
        element.matches(':focus-visible') &&
        parseFloat(style.outlineWidth) > 0 &&
        style.outlineStyle !== 'none' &&
        style.outlineColor !== 'transparent'
      );
    }),
    'keyboard focus has a visible authored outline',
  ).toBe(true);
}

async function login(page: Page) {
  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('账号', { exact: false }).fill(username!);
  await page.getByLabel('密码', { exact: false }).fill(password!);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
}

test('recover from failed login and project requests, persist details and revoke the session', async ({
  page,
}, testInfo) => {
  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('账号', { exact: false }).fill(username!);
  await page.getByLabel('密码', { exact: false }).fill(`${password!}-invalid`);
  const failedLogin = page.waitForResponse('/api/v1/session/login');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  expect((await failedLogin).status()).toBe(401);
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page.getByLabel('账号', { exact: false })).toHaveValue(username!);
  await expect(page.getByLabel('密码', { exact: false })).toHaveValue(`${password!}-invalid`);
  await page.getByLabel('密码', { exact: false }).fill(password!);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
  await page.getByRole('button', { name: '创建项目', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '创建项目' });
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(dialog.getByText('请输入项目名称', { exact: true })).toBeVisible();
  const name = `Browser project ${Date.now()}`;
  await dialog.getByLabel('项目名称', { exact: false }).fill(name);
  await page.route('**/api/v1/projects', (route) => route.abort('failed'));
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('无法连接服务');
  await expect(dialog.getByLabel('项目名称', { exact: false })).toHaveValue(name);
  await expect(dialog.getByRole('combobox', { name: '统计时区' })).toBeEnabled();
  await expect(dialog.getByRole('combobox', { name: '统计时区' })).toHaveValue('Asia/Shanghai');
  await page.unroute('**/api/v1/projects');
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole('button', { name, exact: true }).click();
  await expect(
    page.getByRole('dialog', { name: '项目详情' }).getByText('Asia/Shanghai'),
  ).toBeVisible();
  const details = page.getByRole('dialog', { name: '项目详情' });
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await details.getByRole('button', { name: '复制', exact: true }).click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe(await details.locator('code').innerText());
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
  await expect(dialog.getByLabel('项目名称', { exact: false })).toBeFocused();
  await dialog.getByLabel('项目名称', { exact: false }).fill('Unsaved mobile project');
  await page.screenshot({ path: testInfo.outputPath('mobile-create.png'), fullPage: true });
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('combobox', { name: '统计时区' })).toBeFocused();
  await dialog.getByRole('button', { name: '取消', exact: true }).click();
  const confirmation = page.getByRole('dialog', { name: '放弃尚未保存的项目？' });
  await expect(confirmation.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await confirmation.getByRole('button', { name: '继续编辑' }).click();
  await expect(confirmation).not.toBeVisible();
  await expect(dialog.getByLabel('项目名称', { exact: false })).toBeFocused();
  await expect(dialog.getByLabel('项目名称', { exact: false })).toHaveValue(
    'Unsaved mobile project',
  );
  await page.keyboard.press('Escape');
  await expect(confirmation.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(confirmation).not.toBeVisible();
  await expect(dialog.getByLabel('项目名称', { exact: false })).toBeFocused();
  await expect(dialog.getByLabel('项目名称', { exact: false })).toHaveValue(
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

test('native fields, keyboard containment, navigation guards and busy dismissal remain recoverable', async ({
  page,
}) => {
  await login(page);
  await expect(page.locator('section[aria-label="项目列表"]')).toBeVisible();
  const refresh = page.getByRole('button', { name: '刷新', exact: true });
  let releaseRefresh!: () => void;
  let refreshGate = new Promise<void>((resolve) => {
    releaseRefresh = resolve;
  });
  let refreshRequests = 0;
  await page.route('**/api/v1/projects?*', async (route) => {
    refreshRequests++;
    await refreshGate;
    await route.continue();
  });
  await refresh.focus();
  await page.keyboard.press('Enter');
  await expect(refresh).toBeDisabled();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Space');
  expect(refreshRequests).toBe(1);
  releaseRefresh();
  await expect(refresh).toBeEnabled();
  await expect(refresh).toBeFocused();
  refreshGate = new Promise<void>((resolve) => {
    releaseRefresh = resolve;
  });
  await page.keyboard.press('Enter');
  await expect(refresh).toBeDisabled();
  const otherControl = page.getByRole('button', { name: '创建项目', exact: true });
  await otherControl.focus();
  releaseRefresh();
  await expect(refresh).toBeEnabled();
  await expect(otherControl).toBeFocused();
  expect(refreshRequests).toBe(2);
  await page.unroute('**/api/v1/projects?*');
  // Give Back an in-app destination without creating another authentication session.
  await page.getByRole('link', { name: '项目管理', exact: true }).click();
  const trigger = page.getByRole('button', { name: '创建项目', exact: true });
  await trigger.focus();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Shift+Tab');
  await expectKeyboardFocus(trigger);
  await trigger.evaluate((element) => {
    element.dataset.activations = '0';
    element.addEventListener('click', () => {
      element.dataset.activations = String(Number(element.dataset.activations) + 1);
    });
  });
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: '创建项目', exact: true });
  await expect(dialog).toBeVisible();
  await expect(trigger).toHaveAttribute('data-activations', '1');
  await dialog.getByRole('button', { name: '取消', exact: true }).click();
  await expect(trigger).toBeFocused();
  await page.keyboard.press('Space');
  await expect(dialog).toBeVisible();
  await expect(trigger).toHaveAttribute('data-activations', '2');
  const name = dialog.getByLabel('项目名称', { exact: false });
  const timezone = dialog.getByRole('combobox', { name: '统计时区' });
  await timezone.click();
  await dialog.locator('label[for="project-name"]').click();
  await expect(name).toBeFocused();
  await page.keyboard.press('Tab');
  await expectKeyboardFocus(timezone);
  await page.keyboard.press('Shift+Tab');
  await expectKeyboardFocus(name);
  await name.fill('   ');
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(name).toHaveAttribute('aria-invalid', 'true');
  const nameError = dialog.getByText('请输入项目名称', { exact: true });
  await expect(nameError).toBeVisible();
  const errorID = await nameError.getAttribute('id');
  expect((await name.getAttribute('aria-describedby'))?.split(' ')).toContain(errorID);
  await expect(nameError).toHaveAttribute('aria-live', 'polite');
  await name.fill(`Native controls ${Date.now()}`);
  await timezone.fill('Invalid/Timezone');
  await dialog.getByRole('button', { name: '创建', exact: true }).click();
  await expect(timezone).toHaveAttribute('aria-invalid', 'true');
  await expect(dialog.getByText('请选择有效的统计时区', { exact: true })).toBeVisible();
  await timezone.fill('Asia/Shanghai');
  await name.focus();
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press('Tab');
    // Chromium may move focus to browser chrome (reported as body),
    // but must never focus an inert background control.
    expect(
      await dialog.evaluate(
        (element) =>
          document.activeElement === document.body || element.contains(document.activeElement),
      ),
    ).toBe(true);
  }
  const confirmation = page.getByRole('dialog', { name: '放弃尚未保存的项目？' });
  await page.goBack();
  await expect(confirmation).toBeVisible();
  await confirmation.getByRole('button', { name: '继续编辑', exact: true }).click();
  await expect(name).toHaveValue(/Native controls/);
  await page.getByRole('link', { name: '项目管理', exact: true }).dispatchEvent('click');
  await expect(confirmation).toBeVisible();
  await confirmation.getByRole('button', { name: '继续编辑', exact: true }).click();
  // Trigger the real shell logout control while a modal is open; modal inertness
  // normally prevents pointer interaction, so dispatch the same click event.
  await page.getByRole('button', { name: '退出登录' }).dispatchEvent('click');
  await expect(confirmation).toBeVisible();
  await confirmation.getByRole('button', { name: '继续编辑', exact: true }).click();
  await expect(page).toHaveURL(/\/projects$/);
  expect((await page.request.get('/api/v1/session')).status()).toBe(200);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let submissions = 0;
  await page.route('**/api/v1/projects', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    submissions++;
    await gate;
    await route.continue();
  });
  await name.focus();
  await page.keyboard.press('Enter');
  await expect.poll(() => submissions).toBe(1);
  await expect(dialog.getByRole('button', { name: '创建', exact: true })).toHaveAttribute(
    'aria-busy',
    'true',
  );
  await expect(dialog.getByRole('button', { name: '取消', exact: true })).toBeDisabled();
  await expect(name).toBeDisabled();
  await expect(timezone).toBeDisabled();
  await dialog
    .getByRole('button', { name: '创建', exact: true })
    .evaluate((button: HTMLButtonElement) => button.click());
  await dialog
    .getByRole('button', { name: '取消', exact: true })
    .evaluate((button: HTMLButtonElement) => button.click());
  await page.keyboard.press('Enter');
  await page.keyboard.press('Space');
  await page.keyboard.press('Escape');
  await expect(dialog).toBeVisible();
  expect(submissions).toBe(1);
  release();
  await expect(dialog).not.toBeVisible();
  await page.unroute('**/api/v1/projects');
  expect(submissions).toBe(1);
  await expect(trigger).toBeFocused();
  const closeToast = page.getByRole('button', { name: '关闭通知', exact: true });
  await closeToast.focus();
  await page.keyboard.press('Enter');
  await expect(closeToast).not.toBeVisible();
  await expect(trigger).toBeFocused();
});

test('an expired real session returns to login and fetches projects after reauthentication', async ({
  page,
}) => {
  await login(page);
  await expect(page.locator('section[aria-label="项目列表"]')).toBeVisible();
  const session = await (await page.request.get('/api/v1/session')).json();
  const revoked = await page.request.delete('/api/v1/session', {
    headers: { 'X-CSRF-Token': session.csrf_token, Origin: 'http://127.0.0.1:5173' },
  });
  expect(revoked.status()).toBe(204);
  // Advance only the browser clock past the documented query stale time, then
  // deliver the visibility event used by React Query's browser focus manager.
  await page.clock.setFixedTime(new Date(Date.now() + 31_000));
  const expired = page.waitForResponse(
    (response) => response.url().endsWith('/api/v1/session') && response.status() === 401,
  );
  await page.evaluate(() => window.dispatchEvent(new Event('visibilitychange')));
  await expired;
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.locator('section[aria-label="项目列表"]')).not.toBeVisible();
  await page.getByLabel('账号', { exact: false }).fill(username!);
  await page.getByLabel('密码', { exact: false }).fill(password!);
  const fetched = page.waitForResponse(
    (response) =>
      response.url().includes('/api/v1/projects?') && response.request().method() === 'GET',
  );
  await page.getByRole('button', { name: '登录', exact: true }).click();
  expect((await fetched).status()).toBe(200);
  await expect(page.locator('section[aria-label="项目列表"]')).toBeVisible();
});
