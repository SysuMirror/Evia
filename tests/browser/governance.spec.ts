import { expect, test, type Page } from '@playwright/test';
import { loginAsE2EUser } from './auth-session';

const username = process.env.EVIA_E2E_USERNAME;
const password = process.env.EVIA_E2E_PASSWORD;
if (!username || !password)
  throw new Error(
    'Set EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD for the dedicated local test account.',
  );
// A separate, already provisioned enabled account used for the member lifecycle.
const memberUsername = process.env.EVIA_GOV_MEMBER_USERNAME;

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
    headers: {
      Origin: 'http://127.0.0.1:5173',
      'X-CSRF-Token': session.csrf_token,
    },
    data: { name, timezone: 'Asia/Shanghai' },
  });
  expect(response.status(), `create project: ${await response.text()}`).toBe(201);
  const project = await response.json();
  return project.id as string;
}

async function openGovernance(page: Page, projectID: string) {
  await page.goto(`/projects/${projectID}/governance?environment=development`);
  await expect(page.getByRole('heading', { name: '项目治理' })).toBeVisible();
}

test('owner edits project settings and reads audit through the real governance API', async ({
  page,
}, testInfo) => {
  await login(page);
  const name = `Governance settings ${Date.now()}`;
  const projectID = await createProject(page, name);
  await openGovernance(page, projectID);

  const settingsTab = page.getByRole('tab', { name: '项目设置' });
  await expect(settingsTab).toBeVisible();
  await expect(page.getByRole('tab', { name: '成员管理' })).toBeVisible();
  await expect(page.getByRole('tab', { name: '审计记录' })).toBeVisible();

  const projectName = page.getByLabel('项目名称');
  const timezone = page.getByLabel('默认时区');
  await expect(projectName).toHaveValue(name);
  await expect(timezone).toHaveValue('Asia/Shanghai');
  await expect(page.getByText('设置修订版本：1')).toBeVisible();

  // Invalid timezone is rejected client-side without sending a write.
  await timezone.fill('Local');
  await page.getByRole('button', { name: '保存设置' }).click();
  await expect(page.getByText(/有效的 IANA 时区/)).toBeVisible();

  await projectName.fill(`${name} renamed`);
  await timezone.fill('UTC');
  const saved = page.waitForResponse(
    (response) => response.url().endsWith('/settings') && response.request().method() === 'PATCH',
  );
  await page.getByRole('button', { name: '保存设置' }).click();
  expect((await saved).status()).toBe(200);
  await expect(page.getByRole('status').filter({ hasText: '项目设置已更新' })).toBeVisible();
  await expect(page.getByText('设置修订版本：2')).toBeVisible();

  await page.screenshot({
    path: testInfo.outputPath('desktop-governance-settings.png'),
    fullPage: true,
  });

  // Audit search covers the just-committed update; an exact action filter narrows rows.
  await page.getByRole('tab', { name: '审计记录' }).click();
  await page.getByRole('button', { name: '查询审计' }).click();
  const auditRegion = page.locator('section[aria-label="项目审计"]');
  await expect(auditRegion.getByText('project.settings_updated')).toBeVisible();

  await page.getByLabel('操作（精确）').fill('project.settings_updated');
  await page.getByRole('button', { name: '查询审计' }).click();
  await expect(auditRegion.getByRole('table')).toBeVisible();
  const actionCells = auditRegion.locator('tbody tr td:nth-child(3) code');
  await expect.poll(async () => actionCells.count()).toBeGreaterThan(0);
  for (const cell of await actionCells.all())
    await expect(cell).toHaveText('project.settings_updated');

  // Reject a reversed window without issuing an invalid query.
  await page.setViewportSize({ width: 390, height: 844 });
  const from = page.getByLabel('开始时间');
  const to = page.getByLabel('结束时间');
  const fromValue = await from.inputValue();
  await from.fill(await to.inputValue());
  await to.fill(fromValue);
  await page.getByRole('button', { name: '查询审计' }).click();
  await expect(
    page.getByRole('alert').filter({ hasText: '结束时间必须晚于开始时间' }),
  ).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath('mobile-governance-audit.png'),
    fullPage: true,
  });
});

test('owner manages a member lifecycle while the last enabled owner stays protected', async ({
  page,
}, testInfo) => {
  test.skip(!memberUsername, 'Requires a provisioned account in EVIA_GOV_MEMBER_USERNAME.');
  await login(page);
  const projectID = await createProject(page, `Governance members ${Date.now()}`);
  await openGovernance(page, projectID);
  await page.getByRole('tab', { name: '成员管理' }).click();

  const membersSurface = page.locator('section[aria-label="项目成员"]');
  // Only the bootstrap owner exists initially; it is the protected last owner.
  await expect(membersSurface.getByText('最后一名所有者，不可降级或移除')).toBeVisible();
  await expect(membersSurface.getByRole('button', { name: '移除' })).toBeDisabled();

  // Add the pre-provisioned account as a viewer.
  await page.getByLabel('账号名称').fill(memberUsername!);
  const addResponse = page.waitForResponse(
    (response) => response.url().endsWith('/members') && response.request().method() === 'POST',
  );
  await page.getByRole('button', { name: '添加成员' }).click();
  expect((await addResponse).status()).toBe(201);
  const memberRow = membersSurface.locator('tr', { hasText: memberUsername! }).first();
  await expect(memberRow).toBeVisible();
  await expect(memberRow.getByRole('combobox')).toHaveValue('viewer');

  // Promote to editor.
  await memberRow.getByRole('combobox').selectOption('editor');
  const roleResponse = page.waitForResponse(
    (response) => response.url().includes('/members/') && response.request().method() === 'PATCH',
  );
  await memberRow.getByRole('button', { name: '应用' }).click();
  expect((await roleResponse).status()).toBe(200);
  await expect(memberRow.getByRole('combobox')).toHaveValue('editor');
  await page.screenshot({
    path: testInfo.outputPath('desktop-governance-members.png'),
    fullPage: true,
  });

  // Remove the member; the owner remains the last enabled owner.
  await memberRow.getByRole('button', { name: '移除' }).click();
  const dialog = page.getByRole('dialog', { name: '移除项目成员' });
  await expect(dialog).toBeVisible();
  const removeResponse = page.waitForResponse(
    (response) => response.url().includes('/members/') && response.request().method() === 'DELETE',
  );
  await dialog.getByRole('button', { name: '确认移除' }).click();
  expect((await removeResponse).status()).toBe(200);
  await expect(memberRow).not.toBeVisible();
  await expect(membersSurface.getByText('最后一名所有者，不可降级或移除')).toBeVisible();

  // The removal is recorded in the project audit.
  await page.getByRole('tab', { name: '审计记录' }).click();
  await page.getByLabel('操作（精确）').fill('project.member_removed');
  await page.getByRole('button', { name: '查询审计' }).click();
  await expect(
    page.locator('section[aria-label="项目审计"]').getByText('project.member_removed'),
  ).toBeVisible();
});
