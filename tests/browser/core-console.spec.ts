import { randomUUID } from 'node:crypto';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import { loginAsE2EUser } from './auth-session';
import { verifySavedAnalysisWorkflow } from './saved-analysis-workflow';
import { verifyBrowserSDKWorkflow } from './browser-sdk-workflow';
import { verifyDashboardWorkflow } from './dashboard-workflow';
import { verifyMachineAccessWorkflow } from './machine-access-workflow';
import { verifyMachineIntegrationWorkflow } from './machine-integration-workflow';

// Credential responses must never enter automatic trace or screenshot artifacts.
test.use({ trace: 'off', screenshot: 'off', video: 'off', reducedMotion: 'reduce' });

test('real console creates, publishes, activates, collects and queries scoped event data', async ({
  page,
  playwright,
}, testInfo) => {
  test.skip(
    process.env.EVIA_E2E_FULL_STACK !== '1',
    'Requires EVIA_E2E_FULL_STACK=1 and real API, ingest/query workers, PostgreSQL, Kafka, ClickHouse and Redis.',
  );
  test.setTimeout(240_000);
  page.setDefaultTimeout(12_000);
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.setViewportSize({ width: 1440, height: 900 });
  const evidence = process.env.EVIA_E2E_ARTIFACT_DIR ?? testInfo.outputPath('evidence');
  await mkdir(evidence, { recursive: true });
  async function screenshot(name: string) {
    const notifications = page.getByRole('button', { name: '关闭通知', exact: true });
    while ((await notifications.count()) && !(await page.getByRole('dialog').count()))
      await notifications.first().click();
    await page.screenshot({ path: join(evidence, name), fullPage: true });
  }
  await loginAsE2EUser(page);
  const entryAsset = await page
    .locator('script[type=module][src]')
    .evaluateAll(
      (scripts) =>
        scripts
          .map((script) => script.getAttribute('src'))
          .find((src) => src === '/src/main.tsx' || src?.startsWith('/assets/')) ?? null,
    );
  expect(entryAsset, 'real application entry script is present').toBeTruthy();
  await writeFile(join(evidence, 'build.json'), JSON.stringify({ entryAsset }, null, 2));
  const name = `Core browser ${Date.now()}`;
  await page.getByRole('button', { name: '创建项目', exact: true }).click();
  const project = page.getByRole('dialog', { name: '创建项目' });
  await project.getByLabel('项目名称', { exact: false }).fill(name);
  const projectCreated = page.waitForResponse(
    (response) =>
      response.url().endsWith('/api/v1/projects') && response.request().method() === 'POST',
  );
  await project.getByRole('button', { name: '创建', exact: true }).click();
  await expect(project).not.toBeVisible();
  const newProject = await (await projectCreated).json();
  await page.goto(`/projects/${newProject.id}/events?environment=development`);
  await expect(page.getByRole('heading', { name: '事件定义', exact: true })).toBeVisible();
  const projectID = new URL(page.url()).pathname.split('/')[2]!;
  await page.getByRole('button', { name: '创建事件', exact: true }).click();
  const createEvent = page.getByRole('dialog', { name: '创建事件', exact: true });
  await createEvent.getByLabel('事件标识', { exact: false }).fill('browser_action');
  await createEvent.getByLabel('显示名称', { exact: false }).fill('浏览器合成操作');
  await createEvent.getByRole('button', { name: '创建', exact: true }).click();
  const editor = page.getByRole('dialog', { name: '事件定义：浏览器合成操作' });
  await expect(editor).toBeVisible();
  await editor.getByLabel('触发条件', { exact: false }).fill('用户完成合成按钮操作时');
  await editor.getByLabel('事实类型').selectOption('client_observed');
  await editor.getByRole('checkbox', { name: '浏览器', exact: true }).check();
  await editor.getByLabel('新增字段名').fill('page');
  await editor.getByRole('button', { name: '添加字段', exact: true }).click();
  await editor.getByLabel('样例属性 JSON', { exact: false }).fill('{"page":"synthetic"}');
  for (const [summary, label] of [
    ['高级 Schema', '完整 JSON Schema'],
    ['字段元数据', '字段元数据 JSON'],
  ] as const) {
    const disclosure = editor
      .locator('details')
      .filter({ has: page.getByText(summary, { exact: true }) });
    await disclosure.locator('summary').click();
    const input = editor.getByLabel(label, { exact: true });
    const original = await input.inputValue();
    await input.fill('{');
    await disclosure.locator('summary').click();
    await editor.getByRole('button', { name: '保存草稿', exact: true }).click();
    await expect(input).toBeVisible();
    await expect(input).toHaveAttribute('aria-invalid', 'true');
    await input.press('End');
    await input.press('Space');
    await expect(input).toBeVisible();
    await expect(input).toBeFocused();
    await input.fill(original);
    await disclosure.locator('summary').click();
    await expect(input).not.toBeVisible();
  }
  await editor.getByRole('button', { name: '关闭', exact: true }).last().click();
  const discard = page.getByRole('dialog', { name: '放弃未保存的修改？' });
  await expect(discard.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(editor.getByLabel('触发条件', { exact: false })).toHaveValue(
    '用户完成合成按钮操作时',
  );
  const draftURL = `**/api/v1/projects/${projectID}/event-definitions/browser_action/draft`;
  await page.route(draftURL, (route) =>
    route.request().method() === 'PATCH' ? route.abort('failed') : route.continue(),
  );
  await editor.getByRole('button', { name: '保存草稿' }).click();
  await expect(editor.getByRole('alert')).toContainText('无法连接服务');
  await expect(editor.getByLabel('样例属性 JSON', { exact: false })).toHaveValue(
    '{"page":"synthetic"}',
  );
  await page.unroute(draftURL);
  await editor.getByRole('button', { name: '保存草稿' }).click();
  await expect(editor.getByRole('button', { name: '验证已保存样例' })).toBeEnabled();
  await editor.getByRole('button', { name: '验证已保存样例' }).click();
  await expect(editor.getByText(/草稿 r2 的样例验证通过/)).toBeVisible();
  await editor.getByRole('button', { name: '关闭', exact: true }).last().click();
  await screenshot('desktop-catalog.png');
  await page.getByRole('link', { name: '发布管理', exact: true }).click();
  await page.getByRole('button', { name: '创建发布', exact: true }).click();
  const publish = page.getByRole('dialog', { name: '创建不可变发布' });
  await publish.getByRole('checkbox', { name: '选择事件 浏览器合成操作' }).click();
  await expect(publish.getByRole('checkbox', { name: '选择事件 浏览器合成操作' })).toBeChecked();
  await expect(publish).toContainText('r2');
  await publish.getByRole('button', { name: '发布所选草稿' }).click();
  const release = page.getByRole('dialog', { name: '发布详情', exact: true });
  await expect(release.getByText('管道就绪', { exact: true })).toBeVisible({ timeout: 65_000 });
  await expect(
    page.locator('.resource-summary').getByText('尚未激活版本', { exact: true }),
  ).toBeVisible();
  await release.getByRole('button', { name: '激活此版本' }).click();
  const activation = page.getByRole('dialog', { name: '确认激活发布' });
  await expect(activation.getByRole('button', { name: '暂不激活' })).toBeFocused();
  await activation.getByRole('button', { name: '确认激活', exact: true }).click();
  await expect(activation).not.toBeVisible();
  await release.getByRole('button', { name: '关闭', exact: true }).last().click();
  await page.getByRole('link', { name: '接入来源', exact: true }).click();
  await page.getByRole('button', { name: '创建来源', exact: true }).click();
  const source = page.getByRole('dialog', { name: '创建来源', exact: true });
  await source.getByLabel('来源名称', { exact: false }).fill('合成浏览器来源');
  await source
    .getByLabel('允许的 Origin', { exact: false })
    .fill('https://synthetic.evia.test/path');
  await source.getByRole('button', { name: '创建', exact: true }).click();
  await expect(source.getByLabel('允许的 Origin', { exact: false })).toHaveAttribute(
    'aria-invalid',
    'true',
  );
  await source.getByLabel('允许的 Origin', { exact: false }).fill('https://synthetic.evia.test');
  const sourceResponse = page.waitForResponse(
    (response) => response.url().includes('/sources') && response.request().method() === 'POST',
  );
  await source.getByRole('button', { name: '创建', exact: true }).click();
  const sourceValue = await (await sourceResponse).json();
  await expect(source).not.toBeVisible();
  await page
    .getByRole('dialog', { name: '来源详情：合成浏览器来源' })
    .getByRole('button', { name: '完成', exact: true })
    .last()
    .click();
  await screenshot('desktop-sources.png');
  const publicHTTP = await playwright.request.newContext({ baseURL: 'http://127.0.0.1:8080' });
  try {
    expect((await publicHTTP.storageState()).cookies).toEqual([]);
    const events = [0, 1, 2].map((index) => ({
      event_id: randomUUID(),
      event_key: 'browser_action',
      schema_version: 1,
      release_revision: 1,
      occurred_at: new Date(Date.now() - (3 - index) * 1000).toISOString(),
      identity: { uid: index === 2 ? 'synthetic-2' : 'synthetic-1' },
      context: {
        session_id: randomUUID(),
        tab_id: randomUUID(),
        page_view_id: randomUUID(),
        sequence: index + 1,
        route_name: 'Synthetic',
        route_template: '/synthetic',
      },
      sdk: { name: 'synthetic-browser-check', version: '1.0.0' },
      properties: { page: 'synthetic' },
    }));
    const batch = {
      protocol_version: 1,
      batch_id: randomUUID(),
      write_key: sourceValue.write_key,
      sent_at: new Date().toISOString(),
      events,
    };
    expect(
      (
        await publicHTTP.post('/v1/collect', {
          headers: { Origin: 'https://denied.evia.test' },
          data: batch,
        })
      ).status(),
    ).toBe(403);
    const receipt = await publicHTTP.post('/v1/collect', {
      headers: { Origin: 'https://synthetic.evia.test' },
      data: batch,
    });
    expect(receipt.status()).toBe(202);
    expect(
      (await receipt.json()).results.map((result: { status: string }) => result.status),
    ).toEqual(['accepted', 'accepted', 'accepted']);
  } finally {
    await publicHTTP.dispose();
  }
  const touchEventKey = 'attribution_touch';
  {
    const session = await (await page.request.get('/api/v1/session')).json();
    const headers = {
      Origin: 'http://127.0.0.1:5173',
      'X-CSRF-Token': session.csrf_token as string,
    };
    const apiBase = `/api/v1/projects/${projectID}`;
    await page.request.post(`${apiBase}/event-definitions`, {
      headers,
      data: { event_key: touchEventKey, display_name: 'Attribution touch event' },
    });
    const touchDraft = await (
      await page.request.get(`${apiBase}/event-definitions/${touchEventKey}/draft`)
    ).json();
    const touchUpdated = await (
      await page.request.patch(`${apiBase}/event-definitions/${touchEventKey}/draft`, {
        headers: { ...headers, 'If-Match': `"${touchDraft.revision}"` },
        data: {
          display_name: 'Attribution touch event',
          schema: {
            type: 'object',
            additionalProperties: false,
            properties: { channel: { type: 'string', minLength: 1, maxLength: 64 } },
            required: ['channel'],
          },
          trigger: 'Synthetic attribution touchpoint',
          allowed_source_kinds: ['browser', 'server'],
          fact_kind: 'client_observed',
          example_properties: { channel: 'email' },
        },
      })
    ).json();
    const touchValidated = await (
      await page.request.post(`${apiBase}/event-definitions/${touchEventKey}/validate`, {
        headers,
        data: {
          draft_revision: touchUpdated.revision,
          sample_properties: { channel: 'email' },
        },
      })
    ).json();
    expect(touchValidated.valid).toBe(true);
    const activation = await (
      await page.request.get(`${apiBase}/release-activations?environment=development`)
    ).json();
    const touchRelease = await (
      await page.request.post(`${apiBase}/releases`, {
        headers: { ...headers, 'Idempotency-Key': randomUUID() },
        data: {
          environment: 'development',
          expected_active_revision: activation.active_release_revision as number,
          events: [{ event_key: touchEventKey, draft_revision: touchUpdated.revision }],
        },
      })
    ).json();
    await expect
      .poll(
        async () =>
          (await (await page.request.get(`${apiBase}/releases/${touchRelease.release_id}`)).json())
            .state as string,
        { timeout: 30_000 },
      )
      .toBe('published');
  }
  await page.getByRole('link', { name: '洞察分析', exact: true }).click();
  await page.getByRole('link', { name: '高级查询工作台', exact: true }).click();
  await expect(page).toHaveURL(/\/analysis\b/);
  await expect(page.getByRole('tab', { name: '指标', exact: true })).toBeVisible();
  const rangeTrigger = page.getByRole('button', { name: '时间范围', exact: true });
  await rangeTrigger.focus();
  await page.keyboard.press('Enter');
  const range = page.getByRole('dialog', { name: '时间范围', exact: true });
  const start = range.getByLabel('开始时间（UTC）');
  const end = range.getByLabel('结束时间（UTC，不含）');
  await end.fill(await start.inputValue());
  await range.getByRole('button', { name: '完成', exact: true }).click();
  await expect(range).toBeVisible();
  await expect(range.getByRole('alert')).toContainText('开始早于结束');
  await end.fill(new Date(Date.now() + 60_000).toISOString().slice(0, 16));
  await range.getByRole('button', { name: '完成', exact: true }).click();
  await expect(range).not.toBeVisible();
  await expect(rangeTrigger).toBeFocused();
  const metricTab = page.getByRole('tab', { name: '指标', exact: true });
  const activePanel = page.locator(`[id="${await metricTab.getAttribute('aria-controls')}"]`);
  await expect(activePanel.getByRole('button', { name: '运行查询', exact: true })).toBeVisible();
  await page.getByLabel('事件（跨已发布版本）').selectOption('browser_action');
  async function query() {
    const submitted = page.waitForResponse(
      (response) =>
        response.url().endsWith(`/projects/${projectID}/queries`) &&
        response.request().method() === 'POST',
    );
    const received = page.waitForResponse(
      (response) => /\/queries\/[^/]+\/result$/.test(response.url()) && response.status() === 200,
    );
    await page.getByRole('button', { name: '运行查询', exact: true }).click();
    expect((await submitted).status()).toBe(202);
    await received;
    await expect(page.locator('section[aria-label="查询结果表"]')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('button', { name: '运行查询', exact: true })).toBeEnabled();
  }
  await query();
  await expect(page.locator('.metric-value')).toHaveText('3');
  const metricBox = await page.locator('.metric-value').boundingBox();
  expect(
    metricBox && metricBox.y + metricBox.height < 900,
    'metric visible in first viewport',
  ).toBe(true);
  await screenshot('desktop-metrics.png');
  await page.getByRole('combobox', { name: '指标', exact: true }).selectOption('pv');
  await expect(page.getByLabel('事件（跨已发布版本）')).toHaveValue('browser_action');
  await expect(page.getByLabel('事件（跨已发布版本）')).toBeEnabled();
  await query();
  await expect(page.locator('.metric-value')).toHaveText('3');
  await page.getByText('过滤条件（0）', { exact: true }).click();
  const filters = page.getByRole('dialog', { name: '过滤条件', exact: true });
  await page.getByLabel('添加属性字段的事件').selectOption('browser_action');
  await page.getByRole('button', { name: '添加条件', exact: true }).click();
  await page.getByLabel('条件 1 字段', { exact: true }).selectOption('browser_action:1:page');
  await page.getByLabel('条件 1 值', { exact: true }).fill('synthetic');
  await filters.getByRole('button', { name: '完成', exact: true }).click();
  await expect(filters).not.toBeVisible();
  await expect(page.getByRole('button', { name: '过滤条件（1）', exact: true })).toBeFocused();
  await query();
  await expect(page.locator('.metric-value')).toHaveText('3');
  await page.getByRole('button', { name: '过滤条件（1）', exact: true }).click();
  await page.getByRole('button', { name: '删除条件 1', exact: true }).click();
  await filters.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('combobox', { name: '指标', exact: true }).selectOption('uv');
  await query();
  await expect(page.locator('.metric-value')).toHaveText('2');
  await verifySavedAnalysisWorkflow(page, screenshot);
  await verifyDashboardWorkflow(page, screenshot);
  await page.getByRole('link', { name: '高级查询工作台', exact: true }).click();
  await page.getByLabel('事件（跨已发布版本）').selectOption('browser_action');
  await page.getByRole('combobox', { name: '指标', exact: true }).selectOption('uv');
  await page.getByRole('button', { name: '时间范围', exact: true }).click();
  await page
    .getByRole('dialog', { name: '时间范围', exact: true })
    .getByLabel('结束时间（UTC，不含）')
    .fill(new Date(Date.now() + 60_000).toISOString().slice(0, 16));
  await page
    .getByRole('dialog', { name: '时间范围', exact: true })
    .getByRole('button', { name: '完成', exact: true })
    .click();
  await page.getByRole('tab', { name: '趋势', exact: true }).click();
  await query();
  await expect(page.getByRole('img', { name: /UV.*完整数值/ })).toBeVisible();
  await screenshot('desktop-trend.png');
  await page.getByRole('tab', { name: '用户旅程', exact: true }).click();
  await page.getByLabel('原始 UID', { exact: true }).fill('synthetic-1');
  await query();
  await expect(page.locator('section[aria-label="查询结果表"] tbody tr')).toHaveCount(2);
  await page.setViewportSize({ width: 390, height: 844 });
  await screenshot('mobile-journey.png');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole('tab', { name: '有序漏斗', exact: true }).click();
  await page.getByRole('button', { name: '配置漏斗（2 步）', exact: true }).click();
  const funnel = page.getByRole('dialog', { name: '配置有序漏斗', exact: true });
  for (const step of [1, 2])
    await page.getByLabel(`步骤 ${step} 事件`, { exact: true }).selectOption('browser_action');
  await funnel.getByRole('button', { name: '完成', exact: true }).click();
  await query();
  await expect(page.locator('section[aria-label="查询结果表"] tbody tr')).toHaveCount(2);
  await page.getByRole('tab', { name: '留存分析', exact: true }).click();
  await page.getByRole('button', { name: '配置留存', exact: true }).click();
  const retention = page.getByRole('dialog', { name: '配置留存分析', exact: true });
  await retention
    .getByLabel('起始事件（已发布事件）', { exact: true })
    .selectOption('browser_action');
  await retention
    .getByLabel('回访事件（已发布事件）', { exact: true })
    .selectOption('browser_action');
  await retention.getByRole('button', { name: '完成', exact: true }).click();
  await query();
  await expect(page.getByText('期按天留存')).toBeVisible();
  await expect(page.locator('section[aria-label="查询结果表"] tbody tr')).not.toHaveCount(0);
  await expect(page.getByRole('cell', { name: '100.00%' })).toBeVisible();
  await page.getByRole('tab', { name: '主体分群', exact: true }).click();
  await page.getByRole('button', { name: '配置分群', exact: true }).click();
  const segment = page.getByRole('dialog', { name: '配置主体分群', exact: true });
  await segment.getByLabel('分群事件（已发布）', { exact: true }).selectOption('browser_action');
  await segment
    .getByLabel('分群标量字段', { exact: true })
    .selectOption({ label: 'browser_action:1:page (string)' });
  await segment.getByRole('button', { name: '完成', exact: true }).click();
  await query();
  await expect(page.getByText('按 page 分群')).toBeVisible();
  await expect(page.locator('section[aria-label="查询结果表"] tbody tr')).not.toHaveCount(0);
  await expect(page.getByRole('cell', { name: '100.00%' })).toBeVisible();
  await page.getByRole('tab', { name: '聚合路径', exact: true }).click();
  await page.getByRole('button', { name: '配置路径', exact: true }).click();
  const pathModal = page.getByRole('dialog', { name: '配置聚合路径', exact: true });
  await pathModal.getByLabel('路径深度（2 至 8 个事件）', { exact: true }).fill('2');
  await pathModal.getByRole('button', { name: '完成', exact: true }).click();
  await query();
  await expect(page.getByText('深度 2 事件路径')).toBeVisible();
  await expect(page.locator('section[aria-label="查询结果表"] tbody tr')).not.toHaveCount(0);
  await expect(page.getByRole('cell', { name: /→/ })).toBeVisible();
  await page.getByRole('tab', { name: '归因分析', exact: true }).click();
  await page.getByRole('button', { name: '配置归因', exact: true }).click();
  const attributionModal = page.getByRole('dialog', {
    name: '配置归因分析',
    exact: true,
  });
  await attributionModal
    .locator('[data-attribution-conversion]')
    .getByLabel('转化事件（已发布）', { exact: true })
    .selectOption('browser_action');
  await attributionModal
    .locator('[data-attribution-touchpoint]')
    .first()
    .getByLabel('触点 1 事件', { exact: true })
    .selectOption(touchEventKey);
  await attributionModal.getByRole('button', { name: '完成', exact: true }).click();
  await query();
  await expect(page.getByText('末次触点归因')).toBeVisible();
  await expect(page.locator('section[aria-label="查询结果表"] tbody tr')).not.toHaveCount(0);
  // No touchpoint facts are ingested, so every converter lands in the direct bucket.
  await expect(page.getByRole('cell', { name: '__direct__' })).toBeVisible();
  await page.getByRole('tab', { name: '用户旅程', exact: true }).click();
  await page.getByLabel('原始 UID', { exact: true }).fill('synthetic-absent');
  await query();
  await expect(page.getByText('该范围内无匹配结果，不等同于数值零。')).toBeVisible();
  await page.getByRole('button', { name: '查询设置', exact: true }).click();
  await page.getByLabel('最多结果行').fill('99');
  await page
    .getByRole('dialog', { name: '查询设置', exact: true })
    .getByRole('button', { name: '完成', exact: true })
    .click();
  await page.getByLabel('当前环境').selectOption('staging');
  const leaveAnalysis = page.getByRole('dialog', { name: '离开分析查询？' });
  await expect(leaveAnalysis.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await leaveAnalysis.getByRole('button', { name: '继续编辑' }).click();
  await expect(page).toHaveURL(/environment=development/);
  await page.getByLabel('当前环境').selectOption('staging');
  await leaveAnalysis.getByRole('button', { name: '离开页面' }).click();
  await expect(page).toHaveURL(/environment=staging/);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('link', { name: '数据接入', exact: true }).click();
  await page.getByRole('link', { name: '接入来源', exact: true }).click();
  await expect(page).toHaveURL(/\/sources\b/);
  await expect(page.getByText('合成浏览器来源', { exact: true })).not.toBeVisible();
  await verifyBrowserSDKWorkflow(page, projectID);
  await verifyMachineAccessWorkflow(page, screenshot);
  await verifyMachineIntegrationWorkflow(page);
  expect(errors, 'no uncaught browser exceptions').toEqual([]);
});

test.describe('real server-source credential lifecycle', () => {
  // One-time secrets are intentionally excluded from Playwright trace and screenshot artifacts.
  test('preserves an interrupted committed rotation and confirms source disablement', async ({
    page,
    playwright,
  }) => {
    test.skip(
      process.env.EVIA_E2E_FULL_STACK !== '1',
      'Requires EVIA_E2E_FULL_STACK=1 and the real full dependency stack.',
    );
    test.setTimeout(120_000);
    page.setDefaultTimeout(12_000);
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await loginAsE2EUser(page);
    const projectName = `Source lifecycle ${Date.now()}`;
    await page.getByRole('button', { name: '创建项目', exact: true }).click();
    const project = page.getByRole('dialog', { name: '创建项目' });
    await project.getByLabel('项目名称', { exact: false }).fill(projectName);
    const projectCreated = page.waitForResponse(
      (response) =>
        response.url().endsWith('/api/v1/projects') && response.request().method() === 'POST',
    );
    await project.getByRole('button', { name: '创建', exact: true }).click();
    await expect(project).not.toBeVisible();
    const newProject = await (await projectCreated).json();
    await page.goto(`/projects/${newProject.id}/events?environment=development`);
    await page.getByRole('link', { name: '接入来源', exact: true }).click();
    const projectID = new URL(page.url()).pathname.split('/')[2]!;
    await page.getByRole('button', { name: '创建来源', exact: true }).click();
    const create = page.getByRole('dialog', { name: '创建来源', exact: true });
    await create.getByLabel('来源名称', { exact: false }).fill('合成服务端来源');
    await create.getByLabel('来源类型', { exact: false }).selectOption('server');
    const createdResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith(`/projects/${projectID}/sources`) &&
        response.request().method() === 'POST',
    );
    await create.getByRole('button', { name: '创建', exact: true }).click();
    const created = await (await createdResponse).json();
    const issued = page.getByRole('dialog', { name: '保存服务端密钥' });
    const secret = issued.getByLabel('服务端 Bearer 密钥', { exact: false });
    await expect(secret).toHaveAttribute('type', 'password');
    expect(
      await secret.evaluate(
        (element: HTMLInputElement) => element.readOnly && element.value.length === 43,
      ),
    ).toBe(true);
    await issued.getByRole('button', { name: '完成', exact: true }).click();
    const discard = page.getByRole('dialog', { name: '放弃未保存的修改？' });
    await expect(discard.getByRole('button', { name: '继续编辑' })).toBeFocused();
    await discard.getByRole('button', { name: '继续编辑' }).click();
    await issued.getByRole('checkbox', { name: '我已安全保存写入键和服务端密钥' }).check();
    await issued.getByRole('button', { name: '完成', exact: true }).click();
    await expect(issued).not.toBeVisible();
    const details = page.getByRole('dialog', { name: '来源详情：合成服务端来源' });
    await expect(details).toBeVisible();
    await expect(details.getByLabel('服务端 Bearer 密钥', { exact: false })).toHaveCount(0);
    await details.getByRole('button', { name: '轮换凭据', exact: true }).click();
    const rotation = page.getByRole('dialog', { name: '轮换凭据', exact: true });
    await expect(rotation.getByRole('button', { name: '取消', exact: true })).toBeFocused();
    await rotation.getByLabel('旧凭据重叠期（秒）', { exact: false }).fill('0');
    const rotationPath = `**/api/v1/projects/${projectID}/sources/${created.id}/credential-rotations`;
    const attempts: { key: string | undefined; body: string | null }[] = [];
    let committedRevision: number | undefined;
    let committedWriteKey: string | undefined;
    await page.route(rotationPath, async (route) => {
      attempts.push({
        key: route.request().headers()['idempotency-key'],
        body: route.request().postData(),
      });
      if (attempts.length === 1) {
        // The real backend commits first; only delivery of that response is interrupted.
        const response = await route.fetch();
        expect(response.status()).toBe(201);
        const result = await response.json();
        committedRevision = result.revision;
        committedWriteKey = result.write_key;
        await response.dispose();
        await route.abort('failed');
      } else await route.continue();
    });
    await rotation.getByRole('button', { name: '确认轮换', exact: true }).click();
    await expect(rotation.getByRole('alert')).toContainText('无法连接服务');
    await expect(rotation.getByLabel('旧凭据重叠期（秒）', { exact: false })).toBeDisabled();
    await rotation.getByRole('button', { name: '重试本次轮换', exact: true }).click();
    await expect(issued).toBeVisible();
    expect(attempts).toHaveLength(2);
    expect(attempts[0]!.key).toBeTruthy();
    expect(attempts[1]).toEqual(attempts[0]);
    await expect(secret).toHaveAttribute('type', 'password');
    await issued.getByRole('checkbox', { name: '我已安全保存写入键和服务端密钥' }).check();
    await issued.getByRole('button', { name: '完成', exact: true }).click();
    await expect(issued).not.toBeVisible();
    await page.unroute(rotationPath);
    const read = await page.request.get(`/api/v1/projects/${projectID}/sources/${created.id}`);
    expect(read.status()).toBe(200);
    const rotated = await read.json();
    expect(rotated.revision).toBe(committedRevision);
    expect(rotated.write_key === committedWriteKey).toBe(true);
    expect('server_secret_once' in rotated).toBe(false);
    await details.getByRole('button', { name: '停用来源', exact: true }).click();
    const disable = page.getByRole('dialog', { name: '停用来源？' });
    await expect(disable.getByRole('button', { name: '取消', exact: true })).toBeFocused();
    await disable.getByRole('button', { name: '确认停用', exact: true }).click();
    await expect(disable).not.toBeVisible();
    await expect(
      details.locator('.resource-statuses').getByText('停用', { exact: true }),
    ).toBeVisible();
    const disabled = await (
      await page.request.get(`/api/v1/projects/${projectID}/sources/${created.id}`)
    ).json();
    expect(disabled.status).toBe('disabled');
    expect('server_secret_once' in disabled).toBe(false);
    const publicHTTP = await playwright.request.newContext({ baseURL: 'http://127.0.0.1:8080' });
    try {
      await expect
        .poll(
          async () => {
            const response = await publicHTTP.get(
              `/v1/config?write_key=${encodeURIComponent(rotated.write_key)}&current_release_revision=0`,
            );
            return (await response.json()).code;
          },
          { timeout: 10_000 },
        )
        .toBe('SOURCE_DISABLED');
    } finally {
      await publicHTTP.dispose();
    }
    expect(errors, 'no uncaught browser exceptions').toEqual([]);
  });
});
