import { expect, test } from '@playwright/test';

const projectID = '2a43d99c-1a2b-4c3d-8e9f-1029384756aa';
const permissions = [
  'project.read',
  'project.write',
  'queries.read',
  'content.read',
  'data.erasure',
  'data.export',
  'settings.read',
  'settings.write',
  'metrics.read',
  'machine_principals.manage',
];

test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session')
      return route.fulfill({
        json: {
          user_id: 'a13e7582-ef90-43ac-9989-44bb9f469233',
          display_name: 'Synthetic browser administrator',
          csrf_token: 'workspace-layout-csrf',
        },
      });
    if (path === `/api/v1/projects/${projectID}`)
      return route.fulfill({
        json: {
          id: projectID,
          name: '合成布局项目',
          timezone: 'Asia/Shanghai',
          capabilities: [],
          permissions,
        },
      });
    if (path === `/api/v1/projects/${projectID}/event-definitions`)
      return route.fulfill({ json: { items: [], next_cursor: null } });
    return route.fulfill({
      status: 404,
      json: { code: 'NOT_FOUND', message: '合成路由未提供响应', request_id: 'layout-test' },
    });
  });
});

test('workspace categories and tool rail preserve route access on desktop and mobile', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`/projects/${projectID}/analysis?environment=development`);

  const categories = page.getByRole('navigation', { name: '工作区分类' });
  await expect(categories).toBeVisible();
  await expect(categories.getByRole('link')).toHaveCount(6);
  await expect(categories.getByRole('link', { name: '洞察分析' })).toHaveAttribute(
    'aria-current',
    'location',
  );
  const toolRail = page.getByRole('navigation', { name: '功能导航' });
  await expect(toolRail.getByRole('link', { name: '高级查询工作台' })).toBeVisible();
  await expect(toolRail.getByRole('link', { name: '会话回放' })).toBeVisible();
  await expect(toolRail.getByRole('link', { name: '接入来源' })).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(categories).toBeHidden();
  await expect(toolRail).toBeHidden();
  const menu = page.getByRole('button', { name: '打开工作区菜单' });
  await menu.click();
  await expect(page.getByRole('button', { name: '关闭工作区菜单' })).toHaveAttribute(
    'aria-expanded',
    'true',
  );
  for (const destination of [
    '项目总览',
    '事件管理',
    '视口点击分布',
    '高级查询工作台',
    '机器访问',
    '数据导出',
    '复合公式指标',
  ])
    await expect(toolRail.getByRole('link', { name: destination, exact: true })).toBeVisible();
  await toolRail.getByRole('link', { name: '高级查询工作台', exact: true }).click();
  await expect(toolRail).toBeHidden();
  await expect(page.getByRole('button', { name: '打开工作区菜单' })).toHaveAttribute(
    'aria-expanded',
    'false',
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(errors).toEqual([]);
});
