import { expect, type Page } from '@playwright/test';

/** Uses the core workflow's authenticated project and three collected synthetic events. */
export async function verifySavedAnalysisWorkflow(
  page: Page,
  screenshot: (name: string) => Promise<void>,
) {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('link', { name: '保存的分析', exact: true }).click();
  await page.getByRole('button', { name: '新建分析', exact: true }).click();
  const title = `合成操作分析 ${Date.now()}`;
  await page.getByLabel('分析标题', { exact: false }).fill(title);
  await page.getByLabel('展示方式', { exact: true }).selectOption('table');
  await page.getByRole('button', { name: '事件版本 (0)', exact: true }).click();
  const pins = page.getByRole('dialog', { name: '固定事件版本', exact: true });
  await pins.getByRole('button', { name: '添加事件版本', exact: true }).click();
  await pins.getByLabel('第 1 个事件', { exact: true }).selectOption('browser_action');
  await pins.getByLabel('第 1 个版本', { exact: true }).selectOption('1');
  await pins.getByRole('button', { name: '应用配置', exact: true }).click();
  await page.getByRole('button', { name: '指标 (1)', exact: true }).click();
  const metrics = page.getByRole('dialog', { name: '指标与主体', exact: true });
  await metrics.getByLabel('身份主体', { exact: true }).selectOption('uid');
  await metrics.getByRole('button', { name: '添加指标', exact: true }).click();
  await metrics.getByLabel('指标 2 运算', { exact: true }).selectOption('unique');
  await metrics.getByRole('button', { name: '应用配置', exact: true }).click();
  await page.getByRole('button', { name: '创建分析', exact: true }).click();
  await expect(page.getByRole('button', { name: '校验已保存草稿', exact: true })).toBeEnabled();
  await screenshot('desktop-saved-analysis-editor.png');

  async function publish(loseResponse = false) {
    await page.getByRole('button', { name: '校验已保存草稿', exact: true }).click();
    const review = page.getByRole('dialog', { name: '发布前校验', exact: true });
    await expect(review.getByText('校验通过', { exact: true })).toBeVisible();
    await expect(
      review.getByRole('button', { name: '发布不可变版本', exact: true }),
    ).toBeDisabled();
    await review
      .getByRole('checkbox', { name: '已核对语义差异与固定事件版本', exact: true })
      .check();
    const versionsURL = '**/saved-analyses/*/versions';
    const attempts: { key: string | undefined; body: string | null }[] = [];
    if (loseResponse)
      await page.route(versionsURL, async (route) => {
        if (route.request().method() !== 'POST') return route.continue();
        attempts.push({
          key: route.request().headers()['idempotency-key'],
          body: route.request().postData(),
        });
        if (attempts.length === 1) {
          const committed = await route.fetch();
          expect(committed.status()).toBe(201);
          await committed.dispose();
          await route.abort('failed');
        } else await route.continue();
      });
    await review.getByRole('button', { name: '发布不可变版本', exact: true }).click();
    if (loseResponse) {
      await expect(review.getByRole('alert').filter({ hasText: '无法连接服务' })).toBeVisible();
      await review.getByRole('button', { name: '返回草稿', exact: true }).click();
      await expect(review).not.toBeVisible();
      await page.getByRole('button', { name: '继续确认发布', exact: true }).click();
      await expect(review.getByRole('checkbox')).toBeChecked();
      await review.getByRole('button', { name: '重试发布版本', exact: true }).click();
    }
    await expect(review).not.toBeVisible();
    if (loseResponse) {
      await page.unroute(versionsURL);
      expect(attempts).toHaveLength(2);
      expect(attempts[0]!.key).toBeTruthy();
      expect(attempts[1]).toEqual(attempts[0]);
      await expect(page.getByRole('combobox', { name: '已发布版本', exact: true })).toHaveValue(
        '2',
      );
      await expect(
        page.getByRole('combobox', { name: '已发布版本', exact: true }).locator('option'),
      ).toHaveCount(3);
    }
  }
  await publish();
  await expect(page.getByText('不可变版本 v1', { exact: true })).toBeVisible();
  await screenshot('desktop-saved-analysis-version.png');
  const rangeTrigger = page.getByRole('button', { name: '执行时间范围', exact: true });
  await rangeTrigger.focus();
  await page.keyboard.press('Enter');
  const range = page.getByRole('dialog', { name: '执行时间范围', exact: true });
  await range
    .getByLabel('开始时间（UTC）', { exact: true })
    .fill(new Date(Date.now() - 3600000).toISOString().slice(0, 16));
  await range
    .getByLabel('结束时间（UTC，不含）', { exact: true })
    .fill(new Date(Date.now() + 60000).toISOString().slice(0, 16));
  await range.getByRole('button', { name: '完成', exact: true }).click();
  await expect(rangeTrigger).toBeFocused();
  await page.getByRole('button', { name: '运行已发布版本', exact: true }).click();
  const result = page.getByRole('region', { name: '保存分析结果表', exact: true });
  await expect(result.locator('tbody tr')).toHaveCount(1, { timeout: 20000 });
  await expect(result.locator('tbody td')).toHaveText(['3', '2']);
  await screenshot('desktop-saved-analysis-result.png');

  await page.getByRole('tab', { name: '草稿', exact: true }).click();
  await page.getByLabel('分析标题', { exact: false }).fill(`${title} 修订`);
  await page.getByRole('button', { name: '返回列表', exact: true }).click();
  const leave = page.getByRole('dialog', { name: '放弃未保存的修改？', exact: true });
  await expect(leave.getByRole('button', { name: '继续编辑', exact: true })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByLabel('分析标题', { exact: false })).toHaveValue(`${title} 修订`);
  await page.getByRole('button', { name: '保存草稿', exact: true }).click();
  await publish(true);
  await page.getByRole('combobox', { name: '已发布版本', exact: true }).selectOption('1');
  await expect(page.getByText('不可变版本 v1', { exact: true })).toBeVisible();
  await expect(page.getByRole('region', { name: '执行已发布分析', exact: true })).toContainText(
    title,
  );
  await expect(page.getByRole('region', { name: '执行已发布分析', exact: true })).not.toContainText(
    '修订',
  );
  await page.getByRole('tab', { name: '草稿', exact: true }).click();
  const advancedTrigger = page.getByRole('button', { name: '高级 JSON', exact: true });
  await advancedTrigger.click();
  const advanced = page.getByRole('dialog', { name: '高级 JSON', exact: true });
  const json = advanced.getByLabel('完整分析定义', { exact: true });
  const original = await json.inputValue();
  await expect(json).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(advanced.getByRole('button', { name: '关闭', exact: true }).last()).toBeFocused();
  await json.fill('{invalid');
  await advanced.getByRole('button', { name: '应用配置', exact: true }).click();
  await expect(advanced.getByRole('alert')).toBeVisible();
  await expect(page.getByRole('button', { name: '保存草稿', exact: true })).toBeDisabled();
  await page.keyboard.press('Escape');
  const discard = page.getByRole('dialog', { name: '放弃本节修改？', exact: true });
  await expect(discard.getByRole('button', { name: '继续编辑', exact: true })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(json).toHaveValue('{invalid');
  await json.fill(original);
  await advanced.getByRole('button', { name: '应用配置', exact: true }).click();
  await expect(advancedTrigger).toBeFocused();
  await expect(page.getByLabel('分析标题', { exact: false })).toHaveValue(`${title} 修订`);
  const notices = page.getByRole('button', { name: '关闭通知', exact: true });
  while (await notices.count()) await notices.first().click();
  await page.setViewportSize({ width: 390, height: 844 });
  await advancedTrigger.click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await screenshot('mobile-saved-analysis-editor.png');
  await page.keyboard.press('Escape');
  await page.setViewportSize({ width: 1440, height: 900 });
}
