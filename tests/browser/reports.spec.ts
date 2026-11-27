import { expect, test, type Page, type Route } from '@playwright/test';
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

const analysisID = '44444444-4444-4444-8444-444444444444';
const reportID = '66666666-6666-4666-8666-666666666666';
const now = '2026-11-12T08:00:00Z';

function analysisFixture() {
  return {
    analysis_id: analysisID,
    title: '学习概况分析',
    published_version: 1,
  };
}

function versionFixture() {
  return {
    analysis_id: analysisID,
    version: 1,
    definition: { title: '学习概况分析' },
  };
}

function reportFixture(revision = 1) {
  return {
    report_id: reportID,
    project_id: 'placeholder',
    environment: 'development',
    revision,
    definition: {
      title: '11 月学习行为周报',
      description: '面向教学运营的每周汇总',
      sections: [
        {
          section_id: '77777777-7777-4777-8777-777777777777',
          reading_order: 1,
          section_title: '第一章 学习概况',
          analysis_id: analysisID,
          analysis_version: 1,
        },
      ],
    },
    created_at: now,
    updated_at: now,
  };
}

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

async function mockReportApis(page: Page, projectID: string) {
  const reports: ReturnType<typeof reportFixture>[] = [];
  const collection = new RegExp(`.*/api/v1/projects/${projectID}/reports(\\?.*)?$`);
  const item = new RegExp(`.*/api/v1/projects/${projectID}/reports/[^/?]+(\\?.*)?$`);
  const analyses = new RegExp(`.*/api/v1/projects/${projectID}/saved-analyses(\\?.*)?$`);
  const versions = new RegExp(
    `.*/api/v1/projects/${projectID}/saved-analyses/${analysisID}/versions(\\?.*)?$`,
  );

  await page.route(versions, async (route) =>
    route.fulfill({ json: { items: [versionFixture()], next_cursor: null } }),
  );
  await page.route(analyses, async (route) =>
    route.fulfill({ json: { items: [analysisFixture()], next_cursor: null } }),
  );
  await page.route(item, async (route: Route) => {
    const method = route.request().method();
    if (method === 'PATCH') {
      const target = reports.find((candidate) => candidate.report_id === reportID);
      if (target) {
        target.revision = 2;
        target.definition = route.request().postDataJSON()!.definition;
      }
      return route.fulfill({ json: reportFixture(2) });
    }
    if (method === 'DELETE') {
      const index = reports.findIndex((candidate) => candidate.report_id === reportID);
      if (index >= 0) reports.splice(index, 1);
      return route.fulfill({ status: 204, body: '' });
    }
    return route.fulfill({ json: reportFixture(reports[0]?.revision ?? 1) });
  });
  await page.route(collection, async (route: Route) => {
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON();
      const created = reportFixture(1);
      created.project_id = projectID;
      created.definition = body.definition;
      reports.push(created);
      return route.fulfill({ status: 201, json: created });
    }
    return route.fulfill({
      json: {
        items: reports.map((candidate) => ({ ...candidate, project_id: projectID })),
        next_cursor: null,
      },
    });
  });
}

test('owner creates, edits and deletes a report through the workspace', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Reports workspace ${Date.now()}`);
  await mockReportApis(page, projectID);

  await page.goto(`/projects/${projectID}/reports?environment=development`);
  await expect(page.getByRole('heading', { name: '分析报告' })).toBeVisible();
  await expect(page.getByRole('link', { name: '分析报告' })).toBeVisible();
  await expect(page.getByText('当前环境还没有报告。')).toBeVisible();

  // Client-side validation blocks an empty submission before any write.
  await page.getByRole('button', { name: '新建报告' }).click();
  const dialog = page.getByRole('dialog', { name: '新建分析报告' });
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: '创建报告' }).click();
  await expect(dialog.getByText('请填写报告标题。')).toBeVisible();

  await dialog.getByLabel('报告标题').fill('11 月学习行为周报');
  await dialog.getByLabel('报告描述').fill('面向教学运营的每周汇总');
  await dialog.getByPlaceholder('章节标题').fill('第一章 学习概况');
  await dialog.getByRole('combobox').first().selectOption(analysisID);
  await dialog.getByRole('combobox').nth(1).selectOption('1');

  const createRequest = page.waitForRequest(
    (request) => request.method() === 'POST' && request.url().endsWith('/reports'),
  );
  await dialog.getByRole('button', { name: '创建报告' }).click();
  const post = await createRequest;
  expect(post.headers()['if-match']).toBe('"0"');
  expect(post.headers()['idempotency-key']).toBeTruthy();
  expect(post.headers()['x-csrf-token']).toBeTruthy();
  const postBody = post.postDataJSON();
  expect(postBody.environment).toBe('development');
  expect(postBody.definition.sections).toEqual([
    {
      reading_order: 1,
      section_title: '第一章 学习概况',
      analysis_id: analysisID,
      analysis_version: 1,
    },
  ]);
  await expect(dialog).not.toBeVisible();

  const surface = page.locator('section[aria-label="development 环境报告"]');
  await expect(surface.getByRole('table')).toBeVisible();
  await expect(surface.getByText('11 月学习行为周报')).toBeVisible();
  await expect(surface.getByText('v1')).toBeVisible();

  // Editing pins the optimistic revision and sends a PATCH with the new body.
  await surface.getByRole('button', { name: '编辑' }).click();
  const editDialog = page.getByRole('dialog', { name: /编辑报告/ });
  await expect(editDialog).toBeVisible();
  await expect(editDialog.getByLabel('报告标题')).toHaveValue('11 月学习行为周报');
  await editDialog.getByLabel('报告标题').fill('11 月学习行为周报（修订）');
  const patchRequest = page.waitForRequest(
    (request) => request.method() === 'PATCH' && request.url().includes(`/reports/${reportID}`),
  );
  await editDialog.getByRole('button', { name: '保存修订' }).click();
  const patch = await patchRequest;
  expect(patch.headers()['if-match']).toBe('"1"');
  expect(patch.headers()['idempotency-key']).toBeTruthy();
  expect(patch.postDataJSON().definition.title).toBe('11 月学习行为周报（修订）');
  await expect(editDialog).not.toBeVisible();
  await expect(surface.getByText('v2')).toBeVisible();

  // Delete opens a confirmation dialog and issues a CSRF-protected 204 call.
  await surface.getByRole('button', { name: '删除' }).click();
  const confirm = page.getByRole('dialog', { name: '删除报告' });
  await expect(confirm).toBeVisible();
  const deleteRequest = page.waitForRequest(
    (request) => request.method() === 'DELETE' && request.url().includes(`/reports/${reportID}`),
  );
  await confirm.getByRole('button', { name: '确认删除' }).click();
  const deleted = await deleteRequest;
  expect(deleted.headers()['x-csrf-token']).toBeTruthy();
  await expect(confirm).not.toBeVisible();
  await expect(page.getByText('当前环境还没有报告。')).toBeVisible();
});

test('report editor supports multiple ordered sections and removal', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Reports sections ${Date.now()}`);
  await mockReportApis(page, projectID);

  await page.goto(`/projects/${projectID}/reports?environment=development`);
  await page.getByRole('button', { name: '新建报告' }).click();
  const dialog = page.getByRole('dialog', { name: '新建分析报告' });

  await dialog.getByRole('button', { name: '添加章节' }).click();
  await expect(dialog.getByText('第 2 章')).toBeVisible();
  const rows = dialog.locator('.report-section-row');
  await expect(rows).toHaveCount(2);

  // Removing the only remaining row is blocked; with two rows removal works.
  await rows.first().getByRole('button', { name: '移除' }).click();
  await expect(rows).toHaveCount(1);
  await expect(dialog.getByRole('button', { name: '移除' })).toBeDisabled();

  // A missing pinned version is caught before the request leaves the browser.
  await dialog.getByLabel('报告标题').fill('只有标题的报告');
  await dialog.getByPlaceholder('章节标题').fill('未选版本的章节');
  await dialog.getByRole('button', { name: '创建报告' }).click();
  await expect(dialog.getByText('必须选择一个已发布的分析版本')).toBeVisible();
  await expect(dialog).toBeVisible();
});
