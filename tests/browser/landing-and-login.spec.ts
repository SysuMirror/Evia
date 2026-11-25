import { expect, test } from '@playwright/test';

test('unauthenticated landing and split login adapt cleanly on desktop and mobile', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));

  await page.route('**/api/v1/session', (route) =>
    route.fulfill({
      status: 401,
      json: { code: 'UNAUTHORIZED', message: 'unauthenticated', request_id: 'landing-test' },
    }),
  );

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1 })).toContainText('洞见每一次交互');
  await expect(page.locator('.evia-landing-hero-video')).toHaveAttribute(
    'src',
    '/media/hero-horizon.mp4',
  );
  await expect(page.locator('.evia-brand-img--white')).toHaveAttribute(
    'src',
    '/evia-icon-white.png',
  );
  await expect(page.locator('.evia-brand-img--white')).not.toHaveClass(/is-hidden/);
  await expect(page.locator('.evia-footer-brand img')).toHaveAttribute(
    'src',
    '/evia-icon-white.png',
  );
  await expect(page.locator('.evia-landing-footer')).toContainText(
    `© ${new Date().getFullYear()} Evia 映途`,
  );
  await expect(page.locator('#architecture')).toContainText('隐私不是附加项，而是默认前提');
  const heroCta = page
    .locator('.evia-landing-hero')
    .getByRole('link', { name: '登录控制台', exact: true });
  await expect(heroCta).toBeVisible();
  await page.getByRole('tab', { name: '强类型契约与多环境发布' }).click();
  await expect(page.getByRole('tabpanel')).toContainText("evia.emit('checkout_completed'");
  await page.locator('#architecture').evaluate((el) => {
    el.scrollIntoView({ block: 'start', behavior: 'instant' });
  });
  await expect(page.locator('.evia-landing-header')).toHaveClass(/evia-landing-header--light/);
  await expect(page.locator('#architecture .evia-pillar').first()).toHaveAttribute(
    'data-revealed',
    'true',
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }));
  await expect(page.locator('.evia-landing-header')).not.toHaveClass(/evia-landing-header--solid/);
  await heroCta.click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole('heading', { name: '欢迎回来' })).toBeVisible();
  await expect(page.locator('.auth-showcase')).toBeVisible();
  await expect(page.locator('.auth-showcase-video')).toHaveAttribute(
    'src',
    '/media/auth-studio.mp4',
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  await page.getByRole('link', { name: /返回首页/ }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('驱动确定性增长');

  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page
    .getByRole('banner', { name: '主导航' })
    .getByRole('link', { name: '进入控制台', exact: true })
    .click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.locator('.auth-showcase')).toBeHidden();
  await expect(page.getByLabel('账号', { exact: false })).toBeVisible();
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  expect(errors).toEqual([]);
});

test('authenticated root redirects to projects workspace', async ({ page }) => {
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session')
      return route.fulfill({
        json: {
          user_id: 'a13e7582-ef90-43ac-9989-44bb9f469233',
          display_name: 'Synthetic browser administrator',
          csrf_token: 'landing-auth-csrf',
        },
      });
    if (path === '/api/v1/projects')
      return route.fulfill({ json: { items: [], next_cursor: null } });
    return route.fulfill({
      status: 404,
      json: { code: 'NOT_FOUND', message: 'not found', request_id: 'landing-auth' },
    });
  });

  await page.goto('/');
  await expect(page).toHaveURL(/\/projects$/);
});
