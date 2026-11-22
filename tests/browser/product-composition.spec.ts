import { expect, test } from '@playwright/test';

const projectID = '2a43d99c-1a2b-4c3d-8e9f-1029384756aa';
const now = '2026-11-22T12:00:00.000Z';

const project = {
  id: projectID,
  name: '真实状态项目',
  timezone: 'Asia/Shanghai',
  capabilities: [],
  permissions: ['project.read', 'project.write', 'queries.read', 'content.read'],
};

const diagnostics = {
  project_id: projectID,
  generated_at: now,
  features: { collection_enabled: true, analysis_enabled: true, delivery_enabled: true },
  sources: {
    status: 'ok',
    total: 1,
    truncated: false,
    by_status: [{ name: 'active', count: 1 }],
    by_environment: [{ name: 'development', count: 1 }],
    disabled_sources: [],
  },
  budget: {
    status: 'ok',
    rate_per_second: 100,
    burst: 200,
    environments: [
      { environment: 'development', rate_per_second: 100, burst: 200, override: false },
    ],
  },
  pipeline: {
    status: 'ok',
    proofs: [
      {
        environment: 'development',
        release_id: '55555555-5555-4555-8555-555555555555',
        revision: 3,
        observed_at: '2026-11-22T11:59:00.000Z',
        age_seconds: 60,
        proof_count: 2,
      },
    ],
  },
  rejections: { status: 'ok', window_hours: 24, totals: [], windows: [] },
};

test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === '/api/v1/session')
      return route.fulfill({
        json: {
          user_id: 'a13e7582-ef90-43ac-9989-44bb9f469233',
          display_name: 'Composition reviewer',
          csrf_token: 'composition-csrf',
        },
      });
    if (path === `/api/v1/projects/${projectID}`) return route.fulfill({ json: project });
    if (path === `/api/v1/projects/${projectID}/diagnostics`)
      return route.fulfill({ json: diagnostics });
    if (path === `/api/v1/projects/${projectID}/queries` && route.request().method() === 'POST')
      return route.fulfill({
        status: 202,
        json: {
          query_id: '77777777-7777-4777-8777-777777777777',
          state: 'succeeded',
          created_at: now,
          finished_at: now,
          result_url: `/api/v1/projects/${projectID}/queries/77777777-7777-4777-8777-777777777777/result`,
        },
      });
    if (path.endsWith('/queries/77777777-7777-4777-8777-777777777777/result'))
      return route.fulfill({
        json: {
          query_id: '77777777-7777-4777-8777-777777777777',
          columns: [
            { key: 'x_bin', type: 'integer' },
            { key: 'y_bin', type: 'integer' },
            { key: 'clicks', type: 'integer' },
          ],
          rows: [{ x_bin: 4, y_bin: 2, clicks: 7 }],
          metadata: {
            data_watermark: '2026-11-22T11:58:00.000Z',
            accuracy: 'exact',
            sample_rate: 1,
            coverage: {
              time_range: { from: '2026-11-21T12:00:00.000Z', to: now },
              state: 'provisional',
              known_gaps: ['pipeline_completeness_not_established'],
            },
            cache_hit: false,
            generated_at: now,
          },
        },
      });
    if (path === `/api/v1/projects/${projectID}/content/streams`)
      return route.fulfill({
        json: {
          items: [
            {
              stream_id: 'stream-with-missing-snapshot',
              content_types: ['session_replay_snapshot'],
              actor_types: ['browser'],
              first_received_at: now,
              last_received_at: now,
              item_count: 1,
            },
          ],
        },
      });
    if (path === `/api/v1/projects/${projectID}/content/streams/stream-with-missing-snapshot/items`)
      return route.fulfill({ json: { items: [] } });
    return route.fulfill({
      status: 404,
      json: { code: 'NOT_FOUND', message: '测试未提供此响应', request_id: 'composition-test' },
    });
  });
});

test('project root lands on an honest overview backed by diagnostics', async ({ page }) => {
  await page.goto(`/projects/${projectID}?environment=development`);
  await expect(page).toHaveURL(/\/overview\?environment=development$/);
  await expect(page.getByRole('heading', { name: '项目总览' })).toBeVisible();
  await expect(page.getByRole('heading', { name: '开发环境运行概况' })).toBeVisible();
  await expect(page.getByText('不能代表事实完整性')).toBeVisible();
  await expect(page.getByRole('table', { name: '开发环境最近管道探针' })).toBeVisible();
  await expect(page.getByRole('link', { name: /高级查询工作台/ })).toBeVisible();
});

test('viewport click distribution keeps controls secondary after a real result', async ({
  page,
}) => {
  await page.goto(`/projects/${projectID}/heatmap?environment=development`);
  await expect(page.getByRole('heading', { name: '视口点击分布', exact: true })).toBeVisible();
  await expect(page.getByRole('tablist', { name: '分析类型' })).toHaveCount(0);
  await page.getByLabel('路由模板').fill('/home');
  await page.getByRole('button', { name: '运行查询', exact: true }).click();
  await expect(page.getByRole('img', { name: /视口点击分布/ })).toBeVisible();
  await expect(page.locator('details.analysis-heatmap-query')).not.toHaveAttribute('open', '');
  await expect(page.getByText('查询标识与原始条件')).not.toBeVisible();
});

test('replay failure is exclusive and transport details stay disclosed', async ({ page }) => {
  await page.goto(`/projects/${projectID}/session-replay?environment=development`);
  await page.locator('button.replay-stream-link').click();
  await page.getByRole('button', { name: '加载回放' }).click();
  await expect(page.getByText('无法重建当前回放')).toBeVisible();
  await expect(page.getByRole('button', { name: '播放' })).toHaveCount(0);
  await expect(page.getByLabel('回放进度')).toHaveCount(0);
  await expect(page.locator('iframe[title="会话回放 DOM 快照"]')).toHaveCount(0);
  await expect(page.locator('details.replay-diagnostics')).not.toHaveAttribute('open', '');
});
