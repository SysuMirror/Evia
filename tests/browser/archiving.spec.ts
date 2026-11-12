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

const now = new Date().toISOString();
const manifestID = '33333333-3333-4333-8333-333333333333';
const jobID = '44444444-4444-4444-8444-444444444444';

function manifestFixture(state: 'materialized' | 'failed') {
  const rowCount = state === 'materialized' ? 128 : 0;
  return {
    manifest_id: state === 'materialized' ? manifestID : '55555555-5555-4555-8555-555555555555',
    project_id: '11111111-1111-4111-8111-111111111111',
    environment: 'development',
    retention_days: 90,
    cutoff_at: '2026-08-13T10:00:00.000Z',
    window_start_at: '2026-05-01T00:00:00.000Z',
    window_end_at: '2026-08-12T00:00:00.000Z',
    row_count: rowCount,
    artifact_path: state === 'materialized' ? '/var/lib/evia/archives/x.jsonl' : null,
    artifact_sha256: state === 'materialized' ? 'a'.repeat(64) : null,
    artifact_bytes: state === 'materialized' ? 2048 : null,
    status: state,
    error_code: state === 'failed' ? 'ARCHIVE_ARTIFACT_WRITE_FAILED' : null,
    sweep_id: null,
    created_at: now,
    completed_at: now,
  };
}

function jobFixture(state: string) {
  return {
    job_id: jobID,
    project_id: '11111111-1111-4111-8111-111111111111',
    environment: 'development',
    manifest_id: manifestID,
    name: 'restore november',
    state,
    rows_expected: 128,
    rows_inserted: state === 'completed' ? 128 : 0,
    error_code: null,
    created_at: now,
    updated_at: now,
    finished_at: state === 'queued' ? null : now,
  };
}

test('owner reads archive manifests and drives the backfill lifecycle', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Archiving ${Date.now()}`);

  let cancelled = false;
  await page.route(`**/api/v1/projects/${projectID}/archive-manifests*`, async (route) => {
    await route.fulfill({
      json: {
        items: [manifestFixture('materialized'), manifestFixture('failed')],
        next_cursor: null,
      },
    });
  });
  await page.route(`**/api/v1/projects/${projectID}/backfills*`, async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 201, json: jobFixture('queued') });
      return;
    }
    await route.fulfill({
      json: { items: [jobFixture(cancelled ? 'cancelled' : 'queued')], next_cursor: null },
    });
  });
  await page.route(`**/api/v1/projects/${projectID}/backfills/${jobID}`, async (route) => {
    cancelled = true;
    await route.fulfill({ json: jobFixture('cancelled') });
  });

  await page.goto(`/projects/${projectID}/archiving?environment=development`);
  await expect(page.getByRole('heading', { name: '归档与回补' })).toBeVisible();

  // Owner navigation exposes the lifecycle entry.
  await expect(page.getByRole('link', { name: '归档回补' })).toBeVisible();

  // Archive manifest table renders both states with integrity metadata.
  const manifests = page.locator('section[aria-label="归档清单"]');
  await expect(manifests.getByText('已物化')).toBeVisible();
  await expect(manifests.getByText('失败', { exact: true })).toBeVisible();
  await expect(manifests.getByText('128')).toBeVisible();

  // Backfill table shows the queued job and its cancel action.
  const jobs = page.locator('section[aria-label="回补作业"]');
  await expect(jobs.getByText('排队中')).toBeVisible();
  await expect(jobs.getByRole('button', { name: '取消' })).toBeVisible();

  // Creating a backfill posts the selected manifest with CSRF protection.
  await page.getByLabel('回补名称').fill('restore november');
  await page.locator('#backfill-manifest').selectOption({ index: 1 });
  const createRequest = page.waitForRequest(
    (request) => request.method() === 'POST' && request.url().includes('/backfills'),
  );
  await page.getByRole('button', { name: '发起回补' }).click();
  const posted = await createRequest;
  expect(posted.headers()['x-csrf-token']).toBeTruthy();
  const body = posted.postDataJSON() as { manifest_id: string; name: string; environment: string };
  expect(body.manifest_id).toBe(manifestID);
  expect(body.name).toBe('restore november');
  expect(body.environment).toBe('development');

  // Cancelling the queued job issues the authenticated DELETE and the table
  // settles on the terminal state.
  const cancelRequest = page.waitForRequest(
    (request) => request.method() === 'DELETE' && request.url().includes(`/backfills/${jobID}`),
  );
  await jobs.getByRole('button', { name: '取消' }).click();
  expect((await cancelRequest).headers()['x-csrf-token']).toBeTruthy();
  await expect(jobs.getByText('已取消')).toBeVisible();
  await expect(jobs.getByRole('button', { name: '取消' })).toHaveCount(0);
});
