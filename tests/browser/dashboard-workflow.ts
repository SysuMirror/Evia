import { expect, type Page } from '@playwright/test';
import type {
  DashboardDraft,
  SavedAnalysisDraft,
  SavedAnalysisVersion,
  Session,
} from '../../packages/api-client/src/index';

/** Wait for the chart ResizeObserver to reconcile the requested viewport before asserting final geometry. */
async function waitForDashboardResponsiveLayout(page: Page) {
  const geometry = () =>
    page.evaluate(() => {
      const width = innerWidth,
        documentWidth = document.documentElement.scrollWidth;
      const charts = [
        ...document.querySelectorAll<HTMLElement>(
          '.dashboard-run .saved-analysis-chart[role="img"]',
        ),
      ]
        .slice(0, 12)
        .map((chart) => {
          const box = chart.getBoundingClientRect(),
            card = chart.closest('.evia-dashboard-card')?.getBoundingClientRect(),
            svg = chart.querySelector('svg')?.getBoundingClientRect();
          return {
            x: box.x,
            width: box.width,
            right: box.right,
            cardX: card?.x ?? null,
            cardWidth: card?.width ?? null,
            cardRight: card?.right ?? null,
            clientWidth: chart.clientWidth,
            svgWidth: svg?.width ?? null,
          };
        });
      const ready =
        documentWidth <= width &&
        charts.length > 0 &&
        charts.every(
          (chart) =>
            chart.cardX !== null &&
            chart.cardRight !== null &&
            chart.x >= chart.cardX &&
            chart.right <= chart.cardRight &&
            chart.clientWidth > 0 &&
            chart.svgWidth !== null &&
            Math.abs(chart.svgWidth - chart.clientWidth) <= 1,
        );
      const overflow =
        documentWidth > width
          ? [...document.querySelectorAll('body *')]
              .map((element) => ({ element, box: element.getBoundingClientRect() }))
              .filter(
                ({ element, box }) =>
                  box.width > 0 &&
                  (box.right > width + 1 || box.left < -1) &&
                  !element.closest('dialog:not([open])'),
              )
              .slice(0, 12)
              .map(({ element, box }) => ({
                tag: element.tagName,
                class: element.getAttribute('class'),
                x: box.x,
                width: box.width,
                right: box.right,
                overflowX: getComputedStyle(element).overflowX,
              }))
          : [];
      return { ready, width, documentWidth, charts, overflow };
    });
  const initial = await geometry();
  let settled = initial;
  try {
    await expect
      .poll(
        async () => {
          settled = await geometry();
          return settled.ready;
        },
        {
          message: '看板页面与图表需完成响应式布局，文档和图表均不得溢出',
          timeout: 5000,
          intervals: [16, 32, 50, 100, 200],
        },
      )
      .toBe(true);
  } catch (cause) {
    throw new Error(`Dashboard responsive geometry ${JSON.stringify({ initial, settled })}`, {
      cause,
    });
  }
  if (initial.documentWidth > initial.width)
    console.log('Dashboard responsive geometry', JSON.stringify({ initial, settled }));
}

/** Real API setup and UI execution reuse the core workflow's three synthetic events. */
export async function verifyDashboardWorkflow(
  page: Page,
  screenshot: (name: string) => Promise<void>,
) {
  await page.setViewportSize({ width: 1440, height: 900 });
  const projectID = /\/projects\/([^/]+)/.exec(new URL(page.url()).pathname)![1]!;
  const base = `/api/v1/projects/${projectID}`,
    origin = new URL(page.url()).origin;
  const session = (await (await page.request.get('/api/v1/session')).json()) as Session;
  const headers = { Origin: origin, 'X-CSRF-Token': session.csrf_token };
  const existing = await (
    await page.request.get(`${base}/saved-analyses?environment=development&limit=100`)
  ).json();
  const source = existing.items.find(
    (item: { title: string; published_version: number }) =>
      item.title.startsWith('合成操作分析') && item.published_version > 0,
  );
  expect(source).toBeTruthy();
  const sourceVersion = (await (
    await page.request.get(`${base}/saved-analyses/${source.analysis_id}/versions/1`)
  ).json()) as SavedAnalysisVersion;
  const stamp = Date.now();
  const analysisTitles = [`看板趋势 ${stamp}`, `看板分组 ${stamp}`];
  // Publish independent real analyses; each widget pins a distinct immutable version.
  for (const [index, title] of analysisTitles.entries()) {
    const definition = structuredClone(sourceVersion.definition);
    definition.title = title;
    definition.query.interval = 'hour';
    definition.visualization.kind = index === 0 ? 'line' : 'bar';
    const created = await page.request.post(`${base}/saved-analyses`, {
      headers: { ...headers, 'Idempotency-Key': `dashboard-analysis-${stamp}-${index}` },
      data: { environment: 'development', definition },
    });
    expect(created.status()).toBe(201);
    const draft = (await created.json()) as SavedAnalysisDraft;
    const checked = await page.request.post(
      `${base}/saved-analyses/${draft.analysis_id}/validations`,
      { headers, data: { draft_revision: draft.draft_revision, expected_published_version: 0 } },
    );
    expect(checked.ok()).toBe(true);
    const validation = await checked.json();
    const published = await page.request.post(
      `${base}/saved-analyses/${draft.analysis_id}/versions`,
      {
        headers: { ...headers, 'Idempotency-Key': `dashboard-analysis-publish-${stamp}-${index}` },
        data: {
          draft_revision: validation.draft_revision,
          expected_published_version: validation.published_version,
          validation_hash: validation.validation_hash,
        },
      },
    );
    expect(published.status()).toBe(201);
  }
  await page.getByRole('link', { name: '自定义看板', exact: true }).click();
  await page.getByRole('button', { name: '新建看板', exact: true }).click();
  const title = `合成多图看板 ${stamp}`;
  await page.getByLabel('看板标题', { exact: true }).fill(title);
  for (const [index, analysisTitle] of analysisTitles.entries()) {
    await page.getByRole('button', { name: '添加组件', exact: true }).click();
    const picker = page.getByRole('dialog', { name: '添加看板组件', exact: true });
    await picker
      .getByLabel('保存的分析', { exact: true })
      .selectOption({ label: `${analysisTitle}，最新 v1` });
    await picker.getByLabel('固定分析版本', { exact: true }).selectOption('1');
    await picker
      .getByLabel('组件标题', { exact: true })
      .fill(index === 0 ? '访问趋势' : '访问对比');
    await picker.getByRole('button', { name: '添加组件', exact: true }).click();
  }
  const firstTrigger = page.getByRole('button', { name: '编辑组件 访问趋势', exact: true });
  await firstTrigger.focus();
  await page.keyboard.press('Enter');
  const edit = page.getByRole('dialog', { name: '编辑组件', exact: true });
  await edit.getByRole('button', { name: '增加宽度', exact: true }).focus();
  await page.keyboard.press('Enter');
  await expect(edit.getByRole('status')).toContainText('重叠或超出布局边界');
  await edit.getByRole('button', { name: '减少高度', exact: true }).focus();
  await page.keyboard.press('Enter');
  await expect(edit.getByRole('status')).toContainText('高 4 行');
  await edit.getByRole('button', { name: '完成', exact: true }).click();
  await expect(firstTrigger).toBeFocused();
  const creation = page.waitForResponse(
    (response) => response.request().method() === 'POST' && response.url().endsWith('/dashboards'),
  );
  await page.getByRole('button', { name: '创建看板', exact: true }).click();
  const created = (await (await creation).json()) as DashboardDraft;
  await expect(page.getByRole('button', { name: '校验已保存草稿', exact: true })).toBeEnabled();
  await screenshot('desktop-dashboard-editor.png');
  const publishAttempts: { key: string | undefined; body: string | null }[] = [];
  const publicationURL = '**/dashboards/*/versions';
  await page.route(publicationURL, async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    publishAttempts.push({
      key: route.request().headers()['idempotency-key'],
      body: route.request().postData(),
    });
    if (publishAttempts.length === 1) {
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      await response.dispose();
      await route.abort('failed');
    } else await route.continue();
  });
  async function publish(lost = false) {
    await page.getByRole('button', { name: '校验已保存草稿', exact: true }).click();
    const review = page.getByRole('dialog', { name: '发布前校验', exact: true });
    await expect(
      review.getByRole('button', { name: '发布不可变版本', exact: true }),
    ).toBeDisabled();
    await expect(
      review
        .getByRole('table', { name: '本次发布的精确组件来源版本', exact: true })
        .locator('tbody tr'),
    ).toHaveCount(2);
    await review
      .getByRole('checkbox', { name: '已核对语义差异与固定组件版本', exact: true })
      .check();
    await review.getByRole('button', { name: '发布不可变版本', exact: true }).click();
    if (lost) {
      await expect(review.getByRole('alert').filter({ hasText: '无法连接服务' })).toBeVisible();
      await review.getByRole('button', { name: '返回草稿', exact: true }).click();
      await page.getByRole('button', { name: '继续确认发布', exact: true }).click();
      await review.getByRole('button', { name: '重试发布版本', exact: true }).click();
    }
    await expect(review).not.toBeVisible();
  }
  await publish(true);
  await page.unroute(publicationURL);
  expect(publishAttempts).toHaveLength(2);
  expect(publishAttempts[0]).toEqual(publishAttempts[1]);
  await page.getByRole('button', { name: '统一时间范围', exact: true }).click();
  const range = page.getByRole('dialog', { name: '执行时间范围', exact: true });
  await range
    .getByLabel('开始时间（UTC）', { exact: true })
    .fill(new Date(Date.now() - 3600000).toISOString().slice(0, 16));
  await range
    .getByLabel('结束时间（UTC，不含）', { exact: true })
    .fill(new Date(Date.now() + 60000).toISOString().slice(0, 16));
  await range.getByRole('button', { name: '完成', exact: true }).click();
  const runURL = '**/dashboards/*/versions/*/runs',
    runs: { key: string | undefined; body: string | null }[] = [];
  await page.route(runURL, async (route) => {
    runs.push({
      key: route.request().headers()['idempotency-key'],
      body: route.request().postData(),
    });
    if (runs.length === 1) {
      const response = await route.fetch();
      expect(response.status()).toBe(202);
      await response.dispose();
      await route.abort('failed');
    } else await route.continue();
  });
  await page.getByRole('button', { name: '运行看板', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '运行响应尚未确认' })).toBeVisible();
  await page.getByRole('button', { name: '重试运行看板', exact: true }).click();
  const results = page.getByRole('region', { name: '看板查询结果', exact: true });
  await expect(results.getByRole('img')).toHaveCount(2, { timeout: 25000 });
  await page.unroute(runURL);
  expect(runs).toHaveLength(2);
  expect(runs[0]).toEqual(runs[1]);
  for (const cardTitle of ['访问趋势', '访问对比']) {
    const card = results.getByRole('region', { name: cardTitle, exact: true }).first();
    await card.getByText(/完整结果表（/).click();
    const table = card.getByRole('table', { name: '已发布分析的完整有界结果', exact: true });
    await expect(table.locator('tbody tr')).toHaveCount(1);
    await expect(table.locator('tbody td').last()).toHaveText('2');
  }
  await page.evaluate(() => window.scrollTo(0, 0));
  const desktopCard = results.getByRole('region', { name: '访问趋势', exact: true }).first();
  expect((await desktopCard.boundingBox())!.y).toBeLessThanOrEqual(360);
  await screenshot('desktop-dashboard-result.png');
  await page.setViewportSize({ width: 390, height: 844 });
  await waitForDashboardResponsiveLayout(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.evaluate(() => window.scrollTo(0, 0));
  expect((await desktopCard.boundingBox())!.y).toBeLessThanOrEqual(650);
  for (const img of await results.getByRole('img').all()) {
    const bounds = await img.boundingBox();
    const card = await img
      .locator('xpath=ancestor::section[contains(@class,"evia-dashboard-card")]')
      .boundingBox();
    expect(bounds!.width).toBeLessThanOrEqual(card!.width - 24);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(card!.x + card!.width);
  }
  await screenshot('mobile-dashboard-result.png');
  await page.setViewportSize({ width: 1440, height: 900 });
  // Discover a published property and target one component; its sibling keeps real data.
  await page.getByRole('button', { name: '组件运行筛选 (0)', exact: true }).click();
  const filters = page.getByRole('dialog', { name: '组件运行筛选', exact: true });
  await filters.getByRole('button', { name: '添加运行筛选', exact: true }).click();
  await filters.getByLabel('条件 1 目标组件', { exact: true }).selectOption({ label: '访问趋势' });
  const field = filters.getByLabel('条件 1 字段', { exact: true });
  await expect(field.locator('option')).toContainText(['page']);
  const property = await field
    .locator('option')
    .evaluateAll((options) =>
      options
        .find(
          (option) =>
            option.textContent?.includes('page') &&
            (option as HTMLOptionElement).value.endsWith(':page'),
        )
        ?.getAttribute('value'),
    );
  expect(property).toBeTruthy();
  await field.selectOption(property!);
  await filters.getByLabel('条件 1 值', { exact: true }).fill('not-present-in-synthetic-events');
  await filters.getByRole('button', { name: '应用筛选', exact: true }).click();
  await page.getByRole('button', { name: '运行看板', exact: true }).click();
  const emptyCard = results.getByRole('region', { name: '访问趋势', exact: true }).first();
  await expect(emptyCard.getByText('完整结果表（0 行）', { exact: true })).toBeVisible({
    timeout: 25000,
  });
  const unaffected = results.getByRole('region', { name: '访问对比', exact: true }).first();
  await unaffected.getByText('完整结果表（1 行）', { exact: true }).click();
  await expect(unaffected.getByRole('table').locator('tbody td').last()).toHaveText('2');
  await expect(page.getByRole('button', { name: '组件运行筛选 (1)', exact: true })).toBeVisible();
  // A genuine concurrent writer changes the stored revision while local input survives.
  await page.getByRole('tab', { name: '草稿', exact: true }).click();
  await page.getByLabel('看板标题', { exact: true }).fill(`${title} 本地`);
  const remote = (await (
    await page.request.get(`${base}/dashboards/${created.dashboard_id}/draft`)
  ).json()) as DashboardDraft;
  remote.definition.description = '另一个会话更新了描述';
  const changed = await page.request.patch(`${base}/dashboards/${created.dashboard_id}/draft`, {
    headers: { ...headers, 'If-Match': `"${remote.draft_revision}"` },
    data: { definition: remote.definition },
  });
  expect(changed.status()).toBe(200);
  await page.getByRole('button', { name: '保存草稿', exact: true }).click();
  await expect(page.getByRole('status').filter({ hasText: '远端修订已变化' })).toBeVisible();
  await expect(page.getByLabel('看板标题', { exact: true })).toHaveValue(`${title} 本地`);
  await page.getByRole('button', { name: '读取最新草稿', exact: true }).first().click();
  await page.getByRole('button', { name: '以最新修订继续编辑本地内容', exact: true }).click();
  await page.getByRole('button', { name: '保存草稿', exact: true }).click();
  await expect(page.getByRole('button', { name: '校验已保存草稿', exact: true })).toBeEnabled();
  await publish();
  await expect(page.getByRole('button', { name: '组件运行筛选 (0)', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '组件运行筛选 (0)', exact: true }).click();
  await expect(filters.getByLabel('条件 1 目标组件', { exact: true })).toHaveCount(0);
  await filters.getByRole('button', { name: '返回', exact: true }).click();
  // Apply a real v2 filter, then switch directly while preserving the old run snapshot.
  await page.getByRole('button', { name: '统一时间范围', exact: true }).click();
  await range
    .getByLabel('开始时间（UTC）', { exact: true })
    .fill(new Date(Date.now() - 3600000).toISOString().slice(0, 16));
  await range
    .getByLabel('结束时间（UTC，不含）', { exact: true })
    .fill(new Date(Date.now() + 60000).toISOString().slice(0, 16));
  await range.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('button', { name: '组件运行筛选 (0)', exact: true }).click();
  await filters.getByRole('button', { name: '添加运行筛选', exact: true }).click();
  await filters.getByLabel('条件 1 目标组件', { exact: true }).selectOption({ label: '访问趋势' });
  await filters.getByLabel('条件 1 字段', { exact: true }).selectOption(property!);
  await filters.getByLabel('条件 1 值', { exact: true }).fill('not-present-in-synthetic-events');
  await filters.getByRole('button', { name: '应用筛选', exact: true }).click();
  await page.getByRole('button', { name: '运行看板', exact: true }).click();
  await expect(emptyCard.getByText('完整结果表（0 行）', { exact: true })).toBeVisible({
    timeout: 25000,
  });
  await expect(page.getByRole('combobox', { name: '已发布版本', exact: true })).toBeEnabled();
  await page.getByRole('combobox', { name: '已发布版本', exact: true }).selectOption('1');
  await expect(page.getByText('不可变版本 v1', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '组件运行筛选 (0)', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '组件运行筛选 (0)', exact: true }).click();
  await expect(filters.getByLabel('条件 1 目标组件', { exact: true })).toHaveCount(0);
  await filters.getByRole('button', { name: '返回', exact: true }).click();
  // Version selection retains its own immutable definition.
  await page.getByText('查看该版本的固定配置与依赖', { exact: true }).click();
  await expect(
    page
      .locator('details')
      .filter({ has: page.getByText('查看该版本的固定配置与依赖', { exact: true }) }),
  ).toContainText(`"title": "${title}"`);
  await page.getByRole('tab', { name: '草稿', exact: true }).click();
  await page.getByLabel('看板标题', { exact: true }).fill(`${title} 尚未保存`);
  await page.getByRole('button', { name: '返回列表', exact: true }).click();
  const leave = page.getByRole('dialog', { name: '放弃未保存的修改？', exact: true });
  await expect(leave.getByRole('button', { name: '继续编辑', exact: true })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByLabel('看板标题', { exact: true })).toHaveValue(`${title} 尚未保存`);
  await page.getByRole('button', { name: '返回列表', exact: true }).click();
  await leave.getByRole('button', { name: '放弃修改', exact: true }).click();
}
