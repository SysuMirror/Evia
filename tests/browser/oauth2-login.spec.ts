import { expect, test } from '@playwright/test';

test('login page renders configured OAuth2 provider and starts authorization with next path', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));

  let authorizePayload: Record<string, unknown> | null = null;
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session') {
      return route.fulfill({
        status: 401,
        json: { code: 'SESSION_REQUIRED', message: '请先登录', request_id: 'oauth-test-1' },
      });
    }
    if (path === '/api/v1/auth/oauth2/providers') {
      return route.fulfill({
        json: { items: [{ id: 'unisso', display_name: 'UniSSO' }] },
      });
    }
    if (path === '/api/v1/auth/oauth2/authorize' && route.request().method() === 'POST') {
      authorizePayload = route.request().postDataJSON() as Record<string, unknown>;
      return route.fulfill({
        json: {
          authorization_url:
            '/user/login/oauth2/callback?error=access_denied&state=test-state-redirect',
        },
      });
    }
    return route.fulfill({
      status: 404,
      json: { code: 'NOT_FOUND', message: 'not found', request_id: 'oauth-test-404' },
    });
  });

  await page.goto('/login?next=%2Finvite%3Ftoken%3Dabc12345678901234');
  const ssoButton = page.getByRole('button', { name: '使用 UniSSO 登录' });
  await expect(ssoButton).toBeVisible();

  await ssoButton.click();
  await expect(page).toHaveURL(/\/user\/login\/oauth2\/callback\?error=access_denied/);
  expect(authorizePayload).toEqual({
    provider: 'unisso',
    next: '/invite?token=abc12345678901234',
  });
  await expect(page.getByText('你已取消单点登录授权')).toBeVisible();

  await page.getByRole('button', { name: '返回登录页重新授权' }).click();
  await expect(page).toHaveURL(/\/login$/);
  expect(errors).toEqual([]);
});

test('oauth2 callback exchanges code once, establishes session, and enters projects workspace', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));

  let callbackCalls = 0;
  let lastCallbackBody: Record<string, unknown> | null = null;
  let signedIn = false;

  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session') {
      if (!signedIn) {
        return route.fulfill({
          status: 401,
          json: { code: 'SESSION_REQUIRED', message: '请先登录', request_id: 'oauth-cb-1' },
        });
      }
      return route.fulfill({
        json: {
          user_id: '33333333-3333-4333-8333-333333333333',
          display_name: 'UniSSO 用户',
          csrf_token: 'oauth-csrf-token',
        },
      });
    }
    if (path === '/api/v1/auth/oauth2/callback' && route.request().method() === 'POST') {
      callbackCalls += 1;
      lastCallbackBody = route.request().postDataJSON() as Record<string, unknown>;
      signedIn = true;
      return route.fulfill({
        json: {
          session: {
            user_id: '33333333-3333-4333-8333-333333333333',
            display_name: 'UniSSO 用户',
            csrf_token: 'oauth-csrf-token',
          },
          next_path: '/projects',
        },
      });
    }
    if (path === '/api/v1/projects') {
      return route.fulfill({ json: { items: [], next_cursor: null } });
    }
    return route.fulfill({
      status: 404,
      json: { code: 'NOT_FOUND', message: 'not found', request_id: 'oauth-cb-404' },
    });
  });

  await page.goto('/user/login/oauth2/callback?code=auth-code-99&state=state-token-99');
  await expect(page).toHaveURL(/\/projects$/);
  await expect(page.getByText('暂无项目')).toBeVisible();
  expect(callbackCalls).toBe(1);
  expect(lastCallbackBody).toEqual({ code: 'auth-code-99', state: 'state-token-99' });
  expect(errors).toEqual([]);
});

test('oauth2 callback renders backend errors and missing parameters gracefully', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));

  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session') {
      return route.fulfill({
        status: 401,
        json: { code: 'SESSION_REQUIRED', message: '请先登录', request_id: 'oauth-err-1' },
      });
    }
    if (path === '/api/v1/auth/oauth2/callback' && route.request().method() === 'POST') {
      return route.fulfill({
        status: 400,
        headers: { 'X-Request-ID': 'req-oauth-invalid' },
        json: {
          code: 'OAUTH_CODE_INVALID',
          message: '授权码无效、已过期或已被使用，请重新登录',
          request_id: 'req-oauth-invalid',
        },
      });
    }
    return route.fulfill({
      status: 404,
      json: { code: 'NOT_FOUND', message: 'not found', request_id: 'oauth-err-404' },
    });
  });

  await page.goto('/user/login/oauth2/callback');
  await expect(page.getByText('单点登录回调缺少必要参数')).toBeVisible();

  await page.goto('/user/login/oauth2/callback?code=reused-code&state=valid-state');
  await expect(page.getByText('授权码无效、已过期或已被使用，请重新登录')).toBeVisible();
  await expect(page.getByText('req-oauth-invalid')).toBeVisible();
  expect(errors).toEqual([]);
});
