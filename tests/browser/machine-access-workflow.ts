import { expect, type Page } from '@playwright/test';
import type { MachinePrincipalDetail, Session } from '../../packages/api-client/src/index';

async function assertMachineDetailGeometry(page: Page) {
  await expect
    .poll(
      () =>
        page
          .getByRole('dialog')
          .filter({ has: page.locator('.machine-detail') })
          .evaluate((dialog) => {
            const body = dialog.querySelector<HTMLElement>('.evia-modal-body')!;
            const facts = dialog.querySelector<HTMLElement>('.machine-facts')!;
            const table = dialog.querySelector<HTMLElement>('.evia-table-region')!;
            const bodyBox = body.getBoundingClientRect(),
              factsBox = facts.getBoundingClientRect(),
              tableBox = table.getBoundingClientRect();
            return (
              body.scrollWidth <= body.clientWidth + 1 &&
              factsBox.x >= bodyBox.x &&
              factsBox.right <= bodyBox.right &&
              tableBox.width <= body.clientWidth + 1 &&
              (table.clientWidth >= 780 || table.scrollWidth > table.clientWidth)
            );
          }),
      { message: '机器详情元数据应完整显示，宽凭据表格只在自身区域滚动' },
    )
    .toBe(true);
}

/** Requires automatic trace, screenshot and video capture to be disabled by its caller. */
export async function verifyMachineAccessWorkflow(
  page: Page,
  screenshot: (name: string) => Promise<void>,
) {
  const projectID = /\/projects\/([^/]+)/.exec(new URL(page.url()).pathname)![1]!;
  const base = `/api/v1/projects/${projectID}/machine-principals`;
  const origin = new URL(page.url()).origin;
  const session = (await (await page.request.get('/api/v1/session')).json()) as Session;
  const headers = { Origin: origin, 'X-CSRF-Token': session.csrf_token };
  const name = `构建任务 ${Date.now()}`;
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('link', { name: '机器访问', exact: true }).click();
  await expect(page.getByRole('heading', { name: '机器访问', exact: true })).toBeVisible();
  await expect(page.getByText('全部环境', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '创建机器', exact: true }).click();
  const creation = page.getByRole('dialog', { name: '创建机器', exact: true });
  await expect(creation.getByLabel('机器名称', { exact: false })).toBeFocused();
  await creation.getByRole('button', { name: '创建并签发凭据', exact: true }).click();
  await expect(creation.getByText('请输入机器名称', { exact: true })).toBeVisible();
  await creation.getByLabel('机器名称', { exact: false }).fill('机'.repeat(129));
  await creation.getByRole('button', { name: '创建并签发凭据', exact: true }).click();
  await expect(creation.getByText('机器名称最多为 128 个字符', { exact: true })).toBeVisible();
  await creation.getByLabel('机器名称', { exact: false }).fill('🤖'.repeat(128));
  await creation.getByRole('button', { name: '创建并签发凭据', exact: true }).click();
  await expect(creation.getByLabel('机器名称', { exact: false })).not.toHaveAttribute(
    'aria-invalid',
    'true',
  );
  await expect(creation.getByText('请至少选择一项权限', { exact: true })).toBeVisible();
  await creation.getByLabel('机器名称', { exact: false }).fill(name);
  await expect(creation.getByRole('checkbox', { checked: true })).toHaveCount(0);
  await creation.getByLabel('授权环境', { exact: true }).selectOption('staging');
  await creation.getByRole('checkbox', { name: '读取项目', exact: true }).check();
  await creation.getByRole('checkbox', { name: '读取事件草稿', exact: true }).check();
  for (const label of ['读取指标', '编辑指标草稿', '发布指标版本', '运行指标']) {
    const choice = creation.getByRole('checkbox', { name: label, exact: true });
    await expect(choice).not.toBeChecked();
    await choice.focus();
    await page.keyboard.press('Space');
    await expect(choice).toBeChecked();
  }
  await creation.getByRole('checkbox', { name: '执行查询', exact: true }).check();
  await screenshot('desktop-metric-grants.png');
  await creation.getByRole('button', { name: '稍后继续', exact: true }).click();
  await page.getByRole('link', { name: '事件管理', exact: true }).click();
  const discard = page.getByRole('dialog', { name: '放弃未保存的修改？', exact: true });
  await expect(discard.getByRole('button', { name: '继续编辑', exact: true })).toBeFocused();
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: '继续凭据操作', exact: true }).click();
  await expect(creation.getByLabel('机器名称', { exact: false })).toHaveValue(name);

  const createAttempts: { key?: string; body: string | null }[] = [];
  await page.route(`**${base}`, async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    createAttempts.push({
      key: route.request().headers()['idempotency-key'],
      body: route.request().postData(),
    });
    if (createAttempts.length === 1) {
      const committed = await route.fetch();
      expect(committed.status()).toBe(201);
      await committed.dispose();
      await route.abort('failed');
    } else await route.continue();
  });
  await creation.getByRole('button', { name: '创建并签发凭据', exact: true }).click();
  await expect(creation.getByText('操作结果尚未确认', { exact: true })).toBeVisible();
  await expect(creation.getByLabel('机器名称', { exact: false })).toBeDisabled();
  await creation.getByRole('button', { name: '稍后继续', exact: true }).click();
  await page.getByRole('button', { name: '继续凭据操作', exact: true }).click();
  await creation.getByRole('button', { name: '重试原请求', exact: true }).click();
  await expect(creation).not.toBeVisible();
  await page.unroute(`**${base}`);
  expect(createAttempts).toHaveLength(2);
  expect(createAttempts[0]!.key).toBeTruthy();
  expect(createAttempts[1]).toEqual(createAttempts[0]);
  await expect(page.getByText('凭据记录已确认，未收到可用密钥', { exact: true })).toBeVisible();
  await expect(page.getByRole('dialog', { name: '保存机器密钥', exact: true })).toHaveCount(0);
  const listResponse = await page.request.get(`${base}?limit=100`);
  const listed = await listResponse.json();
  const principal = listed.items.find((item: { name: string }) => item.name === name);
  expect(principal.environment).toBe('staging');
  expect([...principal.permissions].sort()).toEqual(
    [
      'project.read',
      'catalog.drafts.read',
      'metrics.read',
      'metrics.write',
      'metrics.publish',
      'metrics.run',
      'queries.run',
    ].sort(),
  );
  const detailURL = `${base}/${principal.id}`;
  const detail = page.getByRole('dialog', { name: `机器：${name}`, exact: true });
  const rotation = page.getByRole('dialog', { name: '轮换机器凭据', exact: true });
  const secret = page.getByRole('dialog', { name: '保存机器密钥', exact: true });
  async function saveSecret() {
    await expect(secret).toBeVisible();
    await expect(secret.getByLabel('机器密钥', { exact: true })).toHaveAttribute(
      'type',
      'password',
    );
    await secret.getByRole('checkbox', { name: '我已安全保存机器密钥', exact: true }).check();
    await secret.getByRole('button', { name: '完成', exact: true }).click();
    await expect(secret).not.toBeVisible();
    await expect(page.locator('#machine-issued-secret')).toHaveCount(0);
  }
  // The server issued a real secret above. Only explicit new rotation can replace it.
  await page.getByRole('button', { name: '重新轮换凭据', exact: true }).click();
  await expect(rotation.getByText('预发布环境', { exact: true })).toBeVisible();
  await rotation.getByLabel('旧凭据重叠时间（秒）', { exact: false }).fill('301');
  await rotation.getByRole('button', { name: '轮换并签发凭据', exact: true }).click();
  await expect(rotation.getByLabel('旧凭据重叠时间（秒）', { exact: false })).toBeFocused();
  await rotation.getByLabel('旧凭据重叠时间（秒）', { exact: false }).fill('0');
  await rotation.getByRole('button', { name: '轮换并签发凭据', exact: true }).click();
  await saveSecret();
  await expect(detail).toBeVisible();
  await expect(
    detail.getByRole('region', { name: '机器凭据记录', exact: true }).locator('tbody tr'),
  ).toHaveCount(2);
  await assertMachineDetailGeometry(page);
  await screenshot('desktop-machine-access-detail.png');

  const attempts: { key?: string; revision?: string; body: string | null }[] = [];
  await page.route(`**${detailURL}/credential-rotations`, async (route) => {
    attempts.push({
      key: route.request().headers()['idempotency-key'],
      revision: route.request().headers()['if-match'],
      body: route.request().postData(),
    });
    if (attempts.length === 1) {
      const committed = await route.fetch();
      expect(committed.status()).toBe(201);
      await committed.dispose();
      await route.abort('failed');
    } else await route.continue();
  });
  await detail.getByRole('button', { name: '轮换凭据', exact: true }).click();
  await rotation.getByRole('button', { name: '轮换并签发凭据', exact: true }).click();
  await expect(rotation.getByText('操作结果尚未确认', { exact: true })).toBeVisible();
  await rotation.getByRole('button', { name: '重试原请求', exact: true }).click();
  await expect(rotation).not.toBeVisible();
  await page.unroute(`**${detailURL}/credential-rotations`);
  expect(attempts).toHaveLength(2);
  expect(attempts[0]!.key).toBeTruthy();
  expect(attempts[1]).toEqual(attempts[0]);
  await expect(secret).toHaveCount(0);
  await page.getByRole('button', { name: '重新轮换凭据', exact: true }).click();
  await rotation.getByRole('button', { name: '轮换并签发凭据', exact: true }).click();
  await saveSecret();

  // Advance the real revision outside the page, then exercise explicit stale-write recovery.
  await expect(
    detail.getByRole('region', { name: '机器凭据记录', exact: true }).locator('tbody tr'),
  ).toHaveCount(4);
  const before = (await (await page.request.get(detailURL)).json()) as MachinePrincipalDetail;
  const outside = await page.request.post(`${detailURL}/credential-rotations`, {
    headers: {
      ...headers,
      'If-Match': `"${before.principal.revision}"`,
      'Idempotency-Key': crypto.randomUUID(),
    },
    data: { expires_at: new Date(Date.now() + 86400_000).toISOString(), overlap_seconds: 0 },
  });
  expect(outside.status()).toBe(201);
  // Do not inspect, copy, log or capture the response secret.
  await outside.dispose();
  await detail.getByRole('button', { name: '撤销机器', exact: true }).click();
  const revoke = page.getByRole('dialog', { name: '撤销机器', exact: true });
  await expect(revoke.getByRole('button', { name: '取消', exact: true })).toBeFocused();
  await expect(revoke.getByRole('button', { name: '确认撤销', exact: true })).toBeDisabled();
  await revoke.getByLabel('输入机器名称以确认', { exact: true }).fill(name);
  await revoke.getByRole('button', { name: '确认撤销', exact: true }).click();
  await expect(revoke.getByRole('button', { name: '重新读取详情', exact: true })).toBeVisible();
  await revoke.getByRole('button', { name: '重新读取详情', exact: true }).click();
  await expect(revoke.getByLabel('输入机器名称以确认', { exact: true })).toHaveValue('');
  await expect(revoke.getByLabel('输入机器名称以确认', { exact: true })).toBeFocused();
  await revoke.getByRole('button', { name: '取消', exact: true }).click();
  await detail.getByRole('button', { name: '关闭详情', exact: true }).click();
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await page.getByRole('button', { name, exact: true }).click();
  const metadata = (await (await page.request.get(detailURL)).json()) as MachinePrincipalDetail;
  const active = metadata.credentials.find(
    (item) => !item.revoked_at && Date.parse(item.expires_at) > Date.now(),
  )!;
  await detail
    .getByRole('button', { name: `撤销凭据 ${active.id.slice(0, 8)}`, exact: true })
    .click();
  const revokeCredential = page.getByRole('dialog', { name: '撤销机器凭据', exact: true });
  await revokeCredential.getByLabel('输入机器名称以确认', { exact: true }).fill(name);
  await revokeCredential.getByRole('button', { name: '确认撤销', exact: true }).click();
  await expect(revokeCredential).not.toBeVisible();
  await expect(detail.getByRole('row').filter({ hasText: active.id.slice(0, 8) })).toContainText(
    '已撤销',
  );
  await detail.getByRole('button', { name: '撤销机器', exact: true }).click();
  await revoke.getByLabel('输入机器名称以确认', { exact: true }).fill(name);
  await revoke.getByRole('button', { name: '确认撤销', exact: true }).click();
  await expect(revoke).not.toBeVisible();
  await expect(detail.getByRole('button', { name: '轮换凭据', exact: true })).toBeDisabled();
  await page.setViewportSize({ width: 390, height: 844 });
  await assertMachineDetailGeometry(page);
  await detail.locator('.evia-modal-body').evaluate((body) => body.scrollTo({ top: 0, left: 0 }));
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
    .toBe(true);
  await screenshot('mobile-machine-access-detail.png');
  await detail.getByRole('button', { name: '关闭详情', exact: true }).click();
  await screenshot('mobile-machine-access-list.png');
  await page.setViewportSize({ width: 1440, height: 900 });
  await screenshot('desktop-machine-access-list.png');

  // Replace the authenticated session for this same user without navigating away.
  // The old workspace must discard its once-secret local state on the new CSRF identity.
  await page.getByRole('button', { name: '创建机器', exact: true }).click();
  await creation.getByLabel('机器名称', { exact: false }).fill(`${name} 会话边界`);
  await creation.getByRole('checkbox', { name: '读取项目', exact: true }).check();
  const boundaryResponse = page.waitForResponse(
    (response) => response.url().endsWith(base) && response.request().method() === 'POST',
  );
  await creation.getByRole('button', { name: '创建并签发凭据', exact: true }).click();
  expect((await boundaryResponse).status()).toBe(201);
  // Obtain cleanup metadata from a public read, without reading the issued secret.
  const boundaryList = await (await page.request.get(`${base}?limit=100`)).json();
  const boundary = boundaryList.items.find(
    (item: { name: string }) => item.name === `${name} 会话边界`,
  );
  const boundaryID = boundary.id as string;
  const boundaryRevision = boundary.revision as number;
  await expect(secret).toBeVisible();
  const renewedResponse = await page.request.post('/api/v1/session/login', {
    headers: { Origin: origin },
    data: { username: process.env.EVIA_E2E_USERNAME!, password: process.env.EVIA_E2E_PASSWORD! },
  });
  expect(renewedResponse.status()).toBe(200);
  const renewed = (await renewedResponse.json()) as Session;
  expect(renewed.csrf_token !== session.csrf_token).toBe(true);
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const latest = performance
            .getEntriesByType('resource')
            .filter((entry) =>
              ['/api/v1/session', '/api/v1/session/login'].includes(new URL(entry.name).pathname),
            )
            .at(-1) as PerformanceResourceTiming | undefined;
          return latest ? performance.now() - latest.responseEnd : 0;
        }),
      {
        timeout: 31_000,
        intervals: [100, 250, 500, 1000],
        message: '等待浏览器会话查询达到实际的 30 秒重新检查间隔',
      },
    )
    .toBeGreaterThanOrEqual(30_050);
  const rechecked = page.waitForResponse(
    (response) =>
      response.url().endsWith('/api/v1/session') && response.request().method() === 'GET',
  );
  await page.evaluate(() =>
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true })),
  );
  await rechecked;
  await expect(secret).not.toBeVisible();
  await expect(page.locator('#machine-issued-secret')).toHaveCount(0);
  await expect(page.getByRole('button', { name: '创建机器', exact: true })).toBeEnabled();
  const cleanup = await page.request.delete(`${base}/${boundaryID}`, {
    headers: {
      Origin: origin,
      'X-CSRF-Token': renewed.csrf_token,
      'If-Match': `"${boundaryRevision}"`,
    },
  });
  expect(cleanup.status()).toBe(200);
}
