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
    if (path === `/api/v1/projects/${projectID}/overview`)
      return route.fulfill({
        json: {
          project_id: projectID,
          environment: 'development',
          from: '2026-11-15T12:00:00.000Z',
          to: now,
          interval: 'day',
          total_events: 1284,
          unique_sessions: 318,
          identified_users: 146,
          anonymous_ids: 287,
          data_watermark: '2026-11-22T11:58:00.000Z',
          trend: [
            { bucket: '2026-11-20T00:00:00.000Z', events: 322, sessions: 102 },
            { bucket: '2026-11-21T00:00:00.000Z', events: 418, sessions: 129 },
            { bucket: '2026-11-22T00:00:00.000Z', events: 544, sessions: 151 },
          ],
          top_events: [
            { event_key: 'sys.page_view', count: 544 },
            { event_key: 'post.opened', count: 396 },
            { event_key: 'search.performed', count: 188 },
          ],
          metadata: {
            accuracy: 'exact',
            coverage: 'provisional',
            known_gaps: ['pipeline_completeness_not_established'],
            generated_at: now,
          },
        },
      });
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

test('project root lands on an honest real-data overview with secondary diagnostics', async ({
  page,
}) => {
  await page.goto(`/projects/${projectID}?environment=development`);
  await expect(page).toHaveURL(/\/overview\?environment=development$/);
  await expect(page.getByRole('heading', { name: '项目总览' })).toBeVisible();
  await expect(page.getByRole('heading', { name: '近 7 天事件概览' })).toBeVisible();
  await expect(page.getByText('1,284')).toBeVisible();
  await expect(page.getByText('非空 UID，未做身份解析')).toBeVisible();
  await expect(page.getByRole('img', { name: /项目事件趋势/ })).toBeVisible();
  await expect(page.getByRole('heading', { name: '热门事件' })).toBeVisible();
  await expect(page.getByText('sys.page_view')).toBeVisible();
  await expect(page.getByText('覆盖：暂定')).toBeVisible();
  await expect(page.getByRole('heading', { name: '采集诊断' })).toBeVisible();
  await expect(page.getByRole('link', { name: '查看全部诊断' })).toBeVisible();
});

test('overview keeps aggregate and diagnostics failures independent', async ({ page }) => {
  await page.route(`**/api/v1/projects/${projectID}/overview**`, (route) =>
    route.fulfill({
      status: 503,
      json: {
        code: 'QUERY_COST_EXCEEDED',
        message: '聚合超过读取预算',
        request_id: 'overview-test',
      },
    }),
  );
  await page.goto(`/projects/${projectID}/overview?environment=development`);
  await expect(page.getByText('无法读取事件概览')).toBeVisible();
  await expect(page.getByText('聚合超过读取预算')).toBeVisible();
  await expect(page.getByRole('heading', { name: '采集诊断' })).toBeVisible();
  await expect(page.getByText('当前环境来源')).toBeVisible();
});

test('overview shows an honest empty aggregate without hiding diagnostics', async ({ page }) => {
  await page.route(`**/api/v1/projects/${projectID}/overview**`, (route) =>
    route.fulfill({
      json: {
        project_id: projectID,
        environment: 'development',
        from: '2026-11-15T12:00:00.000Z',
        to: now,
        interval: 'day',
        total_events: 0,
        unique_sessions: 0,
        identified_users: 0,
        anonymous_ids: 0,
        data_watermark: null,
        trend: [],
        top_events: [],
        metadata: {
          accuracy: 'exact',
          coverage: 'provisional',
          known_gaps: ['pipeline_completeness_not_established'],
          generated_at: now,
        },
      },
    }),
  );
  await page.goto(`/projects/${projectID}/overview?environment=development`);
  await expect(page.getByText('所选范围内没有可查询事件')).toBeVisible();
  await expect(page.getByText('不以零值推断采集正常')).toBeVisible();
  await expect(page.getByText('当前环境来源')).toBeVisible();
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

test('heatmap supports top element ranking and multi-viewport aggregation in console', async ({
  page,
}) => {
  let postedBody: Record<string, unknown> | undefined;
  await page.route(`**/api/v1/projects/${projectID}/queries`, async (route) => {
    if (route.request().method() === 'POST') {
      postedBody = route.request().postDataJSON() as Record<string, unknown>;
      return route.fulfill({
        status: 202,
        json: {
          query_id: '88888888-8888-4888-8888-888888888888',
          state: 'succeeded',
          created_at: now,
          finished_at: now,
          result_url: `/api/v1/projects/${projectID}/queries/88888888-8888-4888-8888-888888888888/result`,
        },
      });
    }
    return route.fallback();
  });
  await page.route(
    `**/api/v1/projects/${projectID}/queries/88888888-8888-4888-8888-888888888888/result**`,
    (route) =>
      route.fulfill({
        json: {
          query_id: '88888888-8888-4888-8888-888888888888',
          columns: [
            { key: 'element_path', type: 'string' },
            { key: 'clicks', type: 'integer' },
            { key: 'avg_x_ratio', type: 'number' },
            { key: 'avg_y_ratio', type: 'number' },
            { key: 'viewport_count', type: 'integer' },
          ],
          rows: [
            {
              element_path: 'main:nth-of-type(1)>button:nth-of-type(1)',
              clicks: 14,
              avg_x_ratio: 0.425,
              avg_y_ratio: 0.18,
              viewport_count: 3,
            },
          ],
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
      }),
  );

  await page.goto(`/projects/${projectID}/heatmap?environment=development`);
  await page.getByLabel('分析视角').selectOption('elements');
  await page.getByLabel('路由模板').fill('/home');
  await page.getByLabel('视口范围').selectOption('all');
  await page.getByRole('button', { name: '运行查询', exact: true }).click();

  expect(postedBody).toMatchObject({
    kind: 'heatmap',
    heatmap: {
      route_template: '/home',
      mode: 'elements',
      viewport_scope: 'all',
    },
  });
  await expect(page.getByText('main:nth-of-type(1)>button:nth-of-type(1)')).toBeVisible();
  await expect(page.getByText('42.5%')).toBeVisible();
  await expect(page.getByText('18.0%')).toBeVisible();
  await expect(page.getByRole('img', { name: /视口点击分布/ })).toHaveCount(0);
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
