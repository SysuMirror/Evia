import { expect, test, type Page } from '@playwright/test';

const username = process.env.EVIA_E2E_USERNAME;
const password = process.env.EVIA_E2E_PASSWORD;
if (!username || !password)
  throw new Error(
    'Set EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD for the dedicated local test account.',
  );

const EVENT_KEY = 'metrics_console_page';
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

async function sessionHeaders(page: Page) {
  const session = await (await page.request.get('/api/v1/session')).json();
  return {
    Origin: 'http://127.0.0.1:5173',
    'X-CSRF-Token': session.csrf_token as string,
  };
}

/** Create a project and publish its first event version so a metric can pin it. */
async function publishFirstEvent(
  page: Page,
): Promise<{ projectID: string; releaseRevision: number }> {
  const headers = await sessionHeaders(page);
  const project = await (
    await page.request.post('/api/v1/projects', {
      headers,
      data: { name: `Metrics console ${Date.now()}`, timezone: 'UTC' },
    })
  ).json();
  const projectID = project.id as string;
  const base = `/api/v1/projects/${projectID}`;

  await page.request.post(`${base}/event-definitions`, {
    headers,
    data: { event_key: EVENT_KEY, display_name: 'Metrics console page event' },
  });
  const draft = await (
    await page.request.get(`${base}/event-definitions/${EVENT_KEY}/draft`)
  ).json();
  const updated = await (
    await page.request.patch(`${base}/event-definitions/${EVENT_KEY}/draft`, {
      headers: { ...headers, 'If-Match': `"${draft.revision}"` },
      data: {
        display_name: 'Metrics console page event',
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: { page: { type: 'string', minLength: 1, maxLength: 64 } },
          required: ['page'],
        },
        trigger: 'Synthetic console metric verification',
        allowed_source_kinds: ['browser', 'server'],
        fact_kind: 'client_observed',
        example_properties: { page: 'home' },
      },
    })
  ).json();
  const checked = await (
    await page.request.post(`${base}/event-definitions/${EVENT_KEY}/validate`, {
      headers,
      data: { draft_revision: updated.revision, sample_properties: { page: 'home' } },
    })
  ).json();
  expect(checked.valid).toBe(true);

  const release = await (
    await page.request.post(`${base}/releases`, {
      headers: { ...headers, 'Idempotency-Key': crypto.randomUUID() },
      data: {
        environment: 'development',
        expected_active_revision: 0,
        events: [{ event_key: EVENT_KEY, draft_revision: updated.revision }],
      },
    })
  ).json();

  // The release revision is the immutable event version a metric pins. Wait for
  // the real delivery probe so validation can authorize the dependency.
  await expect
    .poll(
      async () => {
        const current = await (
          await page.request.get(`${base}/releases/${release.release_id}`)
        ).json();
        return current.state as string;
      },
      { timeout: 30_000 },
    )
    .toBe('published');
  return { projectID, releaseRevision: release.revision as number };
}

test('owner creates, validates, publishes and runs an atomic metric through the console', async ({
  page,
}, testInfo) => {
  await login(page);
  const { projectID, releaseRevision } = await publishFirstEvent(page);
  expect(releaseRevision).toBeGreaterThanOrEqual(1);

  await page.goto(`/projects/${projectID}/metrics?environment=development`);
  await expect(page.getByRole('heading', { name: '独立指标' })).toBeVisible();
  await page.getByRole('button', { name: '新建指标' }).click();

  await page.getByLabel('指标标题').fill('控制台页面事件次数');
  // The default first event row is replaced with the actually published event.
  await page.getByLabel('事件 1 名称').fill(EVENT_KEY);
  await page.getByLabel('事件 1 版本').fill(String(releaseRevision));
  await page.getByRole('button', { name: '创建草稿' }).click();
  await expect(page.getByRole('button', { name: '校验差异' })).toBeEnabled();

  await page.getByRole('button', { name: '校验差异' }).click();
  await expect(page.getByText('语义校验通过，未执行查询')).toBeVisible();

  await page.getByRole('button', { name: '发布版本' }).click();
  await expect(page.getByText('指标版本已发布')).toBeVisible();

  await page.getByRole('tab', { name: '已发布版本' }).click();
  const versionLink = page.getByRole('button', { name: 'v1' });
  await expect(versionLink).toBeVisible();
  await versionLink.click();

  await expect(page.getByRole('region', { name: '指标运行' })).toBeVisible();
  await expect(page.getByRole('button', { name: '运行 v1' })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath('desktop-metrics-run.png'),
    fullPage: true,
  });
  await page.getByRole('button', { name: '运行 v1' }).click();
  await expect(page.getByText('已完成')).toBeVisible({ timeout: 30_000 });
  // A successful run always renders coverage metadata, even with zero facts.
  await expect(page.getByText(/精度 exact/)).toBeVisible();
});
