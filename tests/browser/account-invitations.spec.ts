import { expect, test, type Page } from '@playwright/test';

const projectID = '2a43d99c-1a2b-4c3d-8e9f-1029384756aa';
const ownerPermissions = [
  'project.read',
  'project.write',
  'queries.read',
  'settings.read',
  'settings.write',
  'members.manage',
  'audit.read',
];

const pageErrors = new WeakMap<Page, string[]>();
test.beforeEach(({ page }) => {
  const errors: string[] = [];
  pageErrors.set(page, errors);
  page.on('pageerror', (error) => errors.push(error.message));
});
test.afterEach(({ page }) => {
  expect(pageErrors.get(page), 'no uncaught browser exceptions').toEqual([]);
});

test('account settings updates display name and rotates password session', async ({ page }) => {
  let displayName = '初始用户';
  let csrfToken = 'csrf-initial';
  const passwordRequests: Array<{ csrf: string | null; body: Record<string, string> }> = [];

  await page.route('**/api/v1/**', async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (path === '/api/v1/session' && req.method() === 'GET') {
      return route.fulfill({
        json: {
          user_id: 'u-1',
          display_name: displayName,
          csrf_token: csrfToken,
          expires_at: '2026-12-01T00:00:00Z',
        },
      });
    }
    if (path === '/api/v1/projects' && req.method() === 'GET') {
      return route.fulfill({ json: { items: [] } });
    }
    if (path === '/api/v1/account' && req.method() === 'GET') {
      return route.fulfill({
        json: {
          user_id: 'u-1',
          username: 'alice',
          display_name: displayName,
          created_at: '2026-11-10T00:00:00Z',
        },
      });
    }
    if (path === '/api/v1/account/profile' && req.method() === 'PATCH') {
      const body = req.postDataJSON() as { display_name: string };
      expect(req.headers()['x-csrf-token']).toBe(csrfToken);
      displayName = body.display_name;
      return route.fulfill({
        json: {
          user_id: 'u-1',
          username: 'alice',
          display_name: displayName,
          created_at: '2026-11-10T00:00:00Z',
        },
      });
    }
    if (path === '/api/v1/account/password' && req.method() === 'POST') {
      passwordRequests.push({
        csrf: req.headers()['x-csrf-token'] ?? null,
        body: req.postDataJSON() as Record<string, string>,
      });
      csrfToken = 'csrf-rotated';
      return route.fulfill({
        json: {
          user_id: 'u-1',
          display_name: displayName,
          csrf_token: csrfToken,
          expires_at: '2026-12-08T00:00:00Z',
        },
      });
    }
    return route.fulfill({ status: 404, json: { code: 'NOT_FOUND', message: 'not mocked' } });
  });

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/projects');
  await page.getByRole('button', { name: '账号设置' }).click();
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByRole('heading', { name: '账号设置' })).toBeVisible();
  await expect(page.getByLabel('登录账号')).toHaveValue('alice');
  await expect(page.getByLabel('登录账号')).toBeDisabled();

  await page.getByLabel('显示名称').fill('Alice 新名称');
  await page.getByRole('button', { name: '保存资料' }).click();
  await expect(page.getByRole('status').filter({ hasText: '显示名称已更新' })).toBeVisible();
  await expect(page.getByRole('button', { name: '账号设置' })).toContainText('Alice 新名称');

  await page.getByLabel('当前密码').fill('old-password-1234');
  await page.locator('#account-new-password').fill('short');
  await page.getByLabel('确认新密码').fill('short');
  await page.getByRole('button', { name: '更新密码' }).click();
  await expect(page.getByRole('alert').filter({ hasText: '12 到 72 位' })).toBeVisible();
  expect(passwordRequests).toHaveLength(0);

  await page.locator('#account-new-password').fill('new-password-5678');
  await page.getByLabel('确认新密码').fill('mismatch-password-5678');
  await page.getByRole('button', { name: '更新密码' }).click();
  await expect(page.getByRole('alert').filter({ hasText: '不一致' })).toBeVisible();
  expect(passwordRequests).toHaveLength(0);

  await page.getByLabel('确认新密码').fill('new-password-5678');
  await page.getByRole('button', { name: '更新密码' }).click();
  await expect(page.getByRole('status').filter({ hasText: '密码已更新' })).toBeVisible();
  expect(passwordRequests).toEqual([
    {
      csrf: 'csrf-initial',
      body: {
        current_password: 'old-password-1234',
        new_password: 'new-password-5678',
      },
    },
  ]);
  await expect(page.getByLabel('当前密码')).toHaveValue('');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('owner creates and revokes project invitations in governance tab', async ({ page }) => {
  const invitations: Array<Record<string, unknown>> = [];

  await page.route('**/api/v1/**', async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (path === '/api/v1/session') {
      return route.fulfill({
        json: {
          user_id: 'u-owner',
          display_name: 'Owner',
          csrf_token: 'csrf-owner',
          expires_at: '2026-12-01T00:00:00Z',
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}`) {
      return route.fulfill({
        json: {
          id: projectID,
          name: '邀请测试项目',
          timezone: 'Asia/Shanghai',
          capabilities: [],
          permissions: ownerPermissions,
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}/settings`) {
      return route.fulfill({
        json: {
          project_id: projectID,
          name: '邀请测试项目',
          timezone: 'Asia/Shanghai',
          revision: 1,
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}/invitations` && req.method() === 'GET') {
      return route.fulfill({ json: { items: invitations, next_cursor: null } });
    }
    if (path === `/api/v1/projects/${projectID}/invitations` && req.method() === 'POST') {
      const body = req.postDataJSON() as {
        role: string;
        note?: string;
        expires_in_hours?: number;
      };
      expect(req.headers()['x-csrf-token']).toBe('csrf-owner');
      const item = {
        id: 'inv-1',
        project_id: projectID,
        role: body.role,
        note: body.note ?? '',
        status: 'pending',
        created_by_user_id: 'u-owner',
        created_by_display_name: 'Owner',
        created_at: '2026-11-25T05:00:00Z',
        expires_at: '2026-12-02T05:00:00Z',
      };
      invitations.unshift(item);
      return route.fulfill({
        status: 201,
        json: {
          invitation: item,
          invite_token: 'tok-generated-secret-123456',
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}/invitations/inv-1` && req.method() === 'DELETE') {
      expect(req.headers()['x-csrf-token']).toBe('csrf-owner');
      const existing = invitations.find((row) => row.id === 'inv-1');
      if (existing) existing.status = 'revoked';
      return route.fulfill({ json: existing });
    }
    return route.fulfill({ status: 404, json: { code: 'NOT_FOUND', message: 'not mocked' } });
  });

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`/projects/${projectID}/governance?environment=development`);
  await page.getByRole('tab', { name: '邀请链接' }).click();
  await expect(page.getByText('还没有生成过邀请链接。')).toBeVisible();

  await page.getByLabel('赋予角色').selectOption('editor');
  await page.getByLabel('有效期').selectOption('720');
  await page.getByLabel('备注（可选）').fill('前端同学');
  await page.getByRole('button', { name: '生成一次性邀请链接' }).click();

  await expect(page.getByLabel('一次性邀请链接')).toHaveValue(
    /\/invite\?token=tok-generated-secret-123456$/,
  );
  await expect(page.getByRole('cell', { name: '前端同学' })).toBeVisible();
  await expect(page.getByRole('cell', { name: '待接受' })).toBeVisible();

  await page.getByRole('button', { name: '撤销', exact: true }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('button', { name: '确认撤销' }).click();
  await expect(page.getByRole('status').filter({ hasText: '邀请链接已撤销' })).toBeVisible();
  await expect(page.getByRole('cell', { name: '已撤销' })).toBeVisible();
});

test('public invite page registers a new user and enters the project', async ({ page }) => {
  let authedSession: Record<string, unknown> | null = null;

  await page.route('**/api/v1/**', async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (path === '/api/v1/session') {
      if (!authedSession) {
        return route.fulfill({ status: 401, json: { code: 'UNAUTHENTICATED', message: '未登录' } });
      }
      return route.fulfill({ json: authedSession });
    }
    if (path === '/api/v1/invitations/preview' && req.method() === 'POST') {
      return route.fulfill({
        json: {
          project_id: projectID,
          project_name: 'Evia Demo 项目',
          role: 'editor',
          note: '欢迎加入',
          expires_at: '2026-12-02T05:00:00Z',
        },
      });
    }
    if (path === '/api/v1/invitations/accept' && req.method() === 'POST') {
      const body = req.postDataJSON() as Record<string, string>;
      expect(body.token).toBe('tok-invite-new-user-123456');
      expect(body.username).toBe('newbie');
      expect(body.display_name).toBe('新成员');
      expect(body.password).toBe('strong-password-1234');
      authedSession = {
        user_id: 'u-new',
        display_name: '新成员',
        csrf_token: 'csrf-new',
        expires_at: '2026-12-02T05:00:00Z',
      };
      return route.fulfill({
        json: {
          project_id: projectID,
          role: 'editor',
          session: authedSession,
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}`) {
      return route.fulfill({
        json: {
          id: projectID,
          name: 'Evia Demo 项目',
          timezone: 'Asia/Shanghai',
          capabilities: [],
          permissions: ['project.read', 'settings.read'],
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}/overview`) {
      return route.fulfill({
        json: {
          project_id: projectID,
          environment: 'development',
          from: '2026-11-18T00:00:00Z',
          to: '2026-11-25T00:00:00Z',
          interval: 'day',
          totals: { accepted_events: 0, active_actors: 0, distinct_events: 0 },
          buckets: [],
          top_events: [],
          freshness: { latest_received_at: null, lag_seconds: null },
        },
      });
    }
    return route.fulfill({ status: 404, json: { code: 'NOT_FOUND', message: 'not mocked' } });
  });

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/invite?token=short');
  await expect(page.getByText('邀请链接缺少有效凭证')).toBeVisible();

  await page.goto('/invite?token=tok-invite-new-user-123456');
  await expect(page.getByRole('heading', { name: '加入项目' })).toBeVisible();
  await expect(page.getByText('Evia Demo 项目')).toBeVisible();
  await expect(page.getByText('欢迎加入')).toBeVisible();

  await page.getByLabel('设置登录账号').fill('newbie');
  await page.getByLabel('显示名称').fill('新成员');
  await page.getByLabel('设置密码').fill('strong-password-1234');
  await page.getByLabel('确认密码').fill('strong-password-1234');
  await page.getByRole('button', { name: '注册并加入项目' }).click();

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/overview`));
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('existing user logs in from invite link, returns to invite page, and joins project', async ({
  page,
}) => {
  let authedSession: Record<string, unknown> | null = null;
  let acceptedCSRF: string | null = null;

  await page.route('**/api/v1/**', async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (path === '/api/v1/session') {
      if (!authedSession) {
        return route.fulfill({ status: 401, json: { code: 'UNAUTHENTICATED', message: '未登录' } });
      }
      return route.fulfill({ json: authedSession });
    }
    if (path === '/api/v1/session/login' && req.method() === 'POST') {
      authedSession = {
        user_id: 'u-existing',
        display_name: '已有用户',
        csrf_token: 'csrf-existing',
        expires_at: '2026-12-02T05:00:00Z',
      };
      return route.fulfill({ json: authedSession });
    }
    if (path === '/api/v1/invitations/preview' && req.method() === 'POST') {
      return route.fulfill({
        json: {
          project_id: projectID,
          project_name: 'Evia Demo 项目',
          role: 'viewer',
          note: '',
          expires_at: '2026-12-02T05:00:00Z',
        },
      });
    }
    if (path === '/api/v1/invitations/accept' && req.method() === 'POST') {
      acceptedCSRF = req.headers()['x-csrf-token'] ?? null;
      return route.fulfill({
        json: {
          project_id: projectID,
          role: 'viewer',
          session: authedSession,
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}`) {
      return route.fulfill({
        json: {
          id: projectID,
          name: 'Evia Demo 项目',
          timezone: 'Asia/Shanghai',
          capabilities: [],
          permissions: ['project.read', 'settings.read'],
        },
      });
    }
    if (path === `/api/v1/projects/${projectID}/overview`) {
      return route.fulfill({
        json: {
          project_id: projectID,
          environment: 'development',
          from: '2026-11-18T00:00:00Z',
          to: '2026-11-25T00:00:00Z',
          interval: 'day',
          totals: { accepted_events: 0, active_actors: 0, distinct_events: 0 },
          buckets: [],
          top_events: [],
          freshness: { latest_received_at: null, lag_seconds: null },
        },
      });
    }
    return route.fulfill({ status: 404, json: { code: 'NOT_FOUND', message: 'not mocked' } });
  });

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/invite?token=tok-invite-existing-123456');
  await page.getByRole('link', { name: '登录后直接加入' }).click();
  await expect(page).toHaveURL(/\/login\?next=%2Finvite%3Ftoken%3Dtok-invite-existing-123456$/);

  await page.getByLabel('账号', { exact: false }).fill('existing');
  await page.getByLabel('密码', { exact: false }).fill('existing-password-1234');
  await page.getByRole('button', { name: '登录', exact: true }).click();

  await expect(page).toHaveURL(/\/invite\?token=tok-invite-existing-123456$/);
  await expect(page.getByText('当前已登录为')).toBeVisible();
  await page.getByRole('button', { name: '接受邀请并加入项目' }).click();

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/overview`));
  expect(acceptedCSRF).toBe('csrf-existing');
});
