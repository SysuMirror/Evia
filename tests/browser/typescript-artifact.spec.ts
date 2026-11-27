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

const releaseID = '33333333-3333-4333-8333-333333333333';

function releaseFixture() {
  return {
    release_id: releaseID,
    revision: 2,
    environment: 'production',
    state: 'published',
    ingestion_ready: true,
    acceleration_state: 'not_requested',
    manifest_hash: 'a'.repeat(64),
  };
}

function artifactFixture() {
  return {
    release_id: releaseID,
    environment: 'production',
    release_revision: 2,
    manifest_hash: 'a'.repeat(64),
    language: 'typescript',
    language_version: '5.0',
    event_count: 1,
    byte_size: 256,
    checksum: 'b'.repeat(64),
    compatibility: [
      {
        event_key: 'lesson_opened',
        schema_version: 1,
        interface_name: 'LessonOpenedProperties',
        valid_identifier: true,
        unsupported_constructs: [],
      },
    ],
    source:
      '// Code generated from an immutable Evia release manifest; DO NOT EDIT.\n' +
      'export interface LessonOpenedProperties {\n  lesson_id: number;\n}\n',
  };
}

test('member opens a release and reads the TypeScript artifact panel', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Artifact ${Date.now()}`);

  await page.route(`**/api/v1/projects/${projectID}/releases*`, async (route) => {
    if (route.request().url().includes('/artifacts/')) return route.continue();
    await route.fulfill({ json: { items: [releaseFixture()], next_cursor: null } });
  });
  await page.route(`**/api/v1/projects/${projectID}/release-activations*`, async (route) => {
    await route.fulfill({
      status: 404,
      json: { code: 'ACTIVATION_NOT_FOUND', message: 'no active release' },
    });
  });
  await page.route(`**/api/v1/projects/${projectID}/releases/${releaseID}`, async (route) => {
    await route.fulfill({ json: releaseFixture() });
  });
  await page.route(
    `**/api/v1/projects/${projectID}/releases/${releaseID}/artifacts/typescript`,
    async (route) => {
      await route.fulfill({ json: artifactFixture() });
    },
  );

  await page.goto(`/projects/${projectID}/releases?environment=production`);
  await page.getByRole('button', { name: '查看详情', exact: true }).click();

  const dialog = page.getByRole('dialog', { name: '发布详情', exact: true });
  await expect(dialog).toBeVisible();

  const panel = dialog.getByRole('region', { name: 'TypeScript 类型产物源码' });
  await expect(panel).toBeVisible();
  await expect(dialog.getByText('LessonOpenedProperties')).toBeVisible();
  await expect(dialog.getByText('typescript ≥ 5.0')).toBeVisible();
  await expect(dialog.getByRole('button', { name: '下载 .ts', exact: true })).toBeVisible();
});

test('artifact panel surfaces generation failures with a retry action', async ({ page }) => {
  await login(page);
  const projectID = await createProject(page, `Artifact Error ${Date.now()}`);

  await page.route(`**/api/v1/projects/${projectID}/releases*`, async (route) => {
    if (route.request().url().includes('/artifacts/')) return route.continue();
    await route.fulfill({ json: { items: [releaseFixture()], next_cursor: null } });
  });
  await page.route(`**/api/v1/projects/${projectID}/release-activations*`, async (route) => {
    await route.fulfill({
      status: 404,
      json: { code: 'ACTIVATION_NOT_FOUND', message: 'no active release' },
    });
  });
  await page.route(`**/api/v1/projects/${projectID}/releases/${releaseID}`, async (route) => {
    await route.fulfill({ json: releaseFixture() });
  });
  let retryClicked = false;
  await page.route(
    `**/api/v1/projects/${projectID}/releases/${releaseID}/artifacts/typescript`,
    async (route) => {
      if (!retryClicked) {
        await route.fulfill({
          status: 503,
          json: { code: 'ARTIFACT_UNAVAILABLE', message: '暂时无法生成类型产物' },
        });
        return;
      }
      await route.fulfill({ json: artifactFixture() });
    },
  );

  await page.goto(`/projects/${projectID}/releases?environment=production`);
  await page.getByRole('button', { name: '查看详情', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '发布详情', exact: true });
  await expect(dialog.getByText('无法生成类型产物', { exact: true })).toBeVisible();
  retryClicked = true;
  await dialog.getByRole('button', { name: '重试', exact: true }).click();
  await expect(dialog.getByText('LessonOpenedProperties')).toBeVisible();
});
