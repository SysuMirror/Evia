import { expect, test, type Page } from '@playwright/test';

const fixtureURL = 'http://127.0.0.1:5174/tests/components/';
const pageErrors = new WeakMap<Page, string[]>();

test.beforeEach(({ page }) => {
  const errors: string[] = [];
  pageErrors.set(page, errors);
  page.on('pageerror', (error) => errors.push(error.message));
});

test.afterEach(({ page }) => {
  expect(pageErrors.get(page), 'no uncaught component fixture exceptions').toEqual([]);
});

test('native fields preserve label, keyboard, readonly and disabled behavior', async ({ page }) => {
  await page.goto(fixtureURL);
  const environment = page.getByRole('combobox', { name: '环境', exact: true });
  await page.getByText('环境', { exact: true }).click();
  await expect(environment).toBeFocused();
  await expect(page.getByLabel('焦点事件')).toHaveText('环境');
  await expect(environment).toHaveAccessibleDescription('使用方向键选择环境。');
  await environment.press('ArrowDown');
  await environment.press('Enter');
  await expect(environment).toHaveValue('testing');
  await expect(page.getByLabel('选择结果')).toHaveText('testing');
  await environment.press('Alt+ArrowDown');
  await environment.press('ArrowDown');
  await environment.press('Enter');
  await expect(environment).toHaveValue('production');
  await environment.press('Tab');
  const notes = page.getByRole('textbox', { name: '说明', exact: true });
  await expect(notes).toBeFocused();
  await notes.fill('修改后的说明\n支持多行');
  await expect(page.getByLabel('说明结果')).toHaveText('修改后的说明\n支持多行');
  await notes.press('Tab');
  const checkbox = page.getByRole('checkbox', { name: '启用记录' });
  await expect(checkbox).toBeFocused();
  await checkbox.press('Space');
  await expect(checkbox).toBeChecked();
  await expect(page.getByLabel('勾选结果')).toHaveText('已启用');
  await page.getByText('启用记录', { exact: true }).click();
  await expect(checkbox).not.toBeChecked();

  for (const [label, value] of [
    ['只读编号', '只读编号 123'],
    ['只读说明', '第一行\n第二行'],
  ] as const) {
    const field = page.getByRole('textbox', { name: label, exact: true });
    await field.focus();
    await expect(field).toBeFocused();
    await expect(field).not.toBeEditable();
    await field.press('ControlOrMeta+A');
    expect(
      await field.evaluate((node) => {
        const control = node as HTMLInputElement | HTMLTextAreaElement;
        return {
          start: control.selectionStart,
          end: control.selectionEnd,
          length: control.value.length,
        };
      }),
    ).toEqual({ start: 0, end: value.length, length: value.length });
    await page.keyboard.type('不可写入');
    await expect(field).toHaveValue(value);
  }
  await page.getByRole('textbox', { name: '只读说明' }).press('Tab');
  await expect(page.getByRole('button', { name: '阻止提交' })).toBeFocused();
  await expect(page.getByRole('textbox', { name: '禁用输入' })).toBeDisabled();
  await expect(page.getByRole('checkbox', { name: '禁用选项' })).toBeDisabled();
  await page
    .getByRole('button', { name: '禁用操作' })
    .evaluate((node) => (node as HTMLButtonElement).click());
  await page
    .getByRole('checkbox', { name: '禁用选项' })
    .evaluate((node) => (node as HTMLInputElement).click());
  await expect(page.getByLabel('禁用激活次数')).toHaveText('0');
  await expect(page.getByRole('checkbox', { name: '禁用选项' })).not.toBeChecked();
  for (const [combinedLabel, disabledLabel, value] of [
    ['禁用且只读输入', '禁用输入', '禁用且只读'],
    ['禁用且只读多行输入', '禁用多行输入', '禁用且只读多行内容'],
  ] as const) {
    const combined = page.getByRole('textbox', { name: combinedLabel, exact: true });
    const disabled = page.getByRole('textbox', { name: disabledLabel, exact: true });
    await expect(combined).toBeDisabled();
    await expect(combined).not.toBeEditable();
    await combined.evaluate((node) => (node as HTMLElement).focus());
    await expect(combined).not.toBeFocused();
    await expect(page.getByRole('button', { name: '阻止提交' })).toBeFocused();
    await page.keyboard.type('不能写入');
    await expect(combined).toHaveValue(value);
    const disabledAppearance = await disabled.evaluate((node) => {
      const style = getComputedStyle(node);
      return { background: style.backgroundColor, cursor: style.cursor };
    });
    expect(
      await combined.evaluate((node) => {
        const style = getComputedStyle(node);
        return { background: style.backgroundColor, cursor: style.cursor };
      }),
    ).toEqual(disabledAppearance);
    expect(disabledAppearance.cursor).toBe('not-allowed');
  }
});

test('buttons forward native events and show keyboard focus and held press feedback', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  await page.getByRole('button', { name: '阻止提交' }).click();
  await expect(page.getByLabel('点击次数')).toHaveText('1');
  await expect(page.getByLabel('提交次数')).toHaveText('0');
  await page.getByRole('button', { name: '提交表单' }).click();
  await expect(page.getByLabel('提交次数')).toHaveText('1');
  await page.getByRole('button', { name: '主要按钮', exact: true }).click();
  await expect(page.getByLabel('提交次数')).toHaveText('1');

  await page.keyboard.press('Tab');
  const primary = page.getByRole('button', { name: '主要按钮', exact: true });
  await primary.focus();
  await expect(primary).toBeFocused();
  const focusOutline = await primary.evaluate((node) => {
    const style = getComputedStyle(node);
    return {
      style: style.outlineStyle,
      width: parseFloat(style.outlineWidth),
      offset: parseFloat(style.outlineOffset),
    };
  });
  expect(focusOutline.style).not.toBe('none');
  expect(focusOutline.width).toBeGreaterThan(0);
  expect(focusOutline.offset).toBeGreaterThanOrEqual(0);

  for (const name of ['主要按钮', '次要按钮', '危险按钮', '幽灵按钮', '链接按钮']) {
    const button = page.getByRole('button', { name, exact: true });
    await button.hover();
    const appearance = () =>
      button.evaluate((node) => {
        const style = getComputedStyle(node);
        return [style.backgroundColor, style.color, style.borderTopColor];
      });
    // Read computed pixels with reduced motion so transitions cannot obscure held feedback.
    await page.emulateMedia({ reducedMotion: 'reduce' });
    const before = await appearance();
    await page.mouse.down();
    await expect.poll(appearance).not.toEqual(before);
    await page.mouse.up();
  }
  const toggle = page.getByRole('button', { name: '切换选择' });
  const toggleBackground = () => toggle.evaluate((node) => getComputedStyle(node).backgroundColor);
  await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  const unselectedAppearance = await toggleBackground();
  await toggle.press('Space');
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  // Even reduced motion has a nonzero transition; observe the rendered state.
  await expect.poll(toggleBackground).not.toBe(unselectedAppearance);
  await toggle.press('Space');
  await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(toggleBackground).toBe(unselectedAppearance);
});

declare global {
  interface Window {
    componentClipboard: { value: string; resolve: () => void; reject: (error: Error) => void }[];
  }
}

test('copy ignores stale value and unmount completions while current work remains recoverable', async ({
  page,
}) => {
  // Clipboard is the only stub: no service or product API is used by this fixture.
  await page.addInitScript(() => {
    const target = window;
    target.componentClipboard = [];
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: (value: string) =>
          new Promise<void>((resolve, reject) => {
            target.componentClipboard.push({ value, resolve, reject });
          }),
      },
    });
  });
  await page.goto(fixtureURL);
  const copy = page.getByRole('button', { name: '复制测试值', exact: true });
  await copy.click();
  await expect(copy).toBeDisabled();
  await expect(copy).toHaveAttribute('aria-busy', 'true');
  await page.getByLabel('待复制值').fill('值 B');
  await expect(copy).toBeEnabled();
  await copy.click();
  expect(await page.evaluate(() => window.componentClipboard.map((item) => item.value))).toEqual([
    '值 A',
    '值 B',
  ]);
  await page.evaluate(() => window.componentClipboard[0]!.resolve());
  await expect(copy).toBeDisabled();
  await expect(page.getByText('已复制', { exact: true })).toHaveCount(0);
  await page.evaluate(() => window.componentClipboard[1]!.resolve());
  await expect(copy).toBeEnabled();
  await expect(page.getByRole('region', { name: '异步复制' }).getByRole('status')).toHaveText(
    '已复制',
  );
  await page.getByRole('button', { name: '关闭通知' }).click();

  await copy.click();
  await page.getByRole('button', { name: '卸载复制控件' }).click();
  await page.evaluate(() =>
    window.componentClipboard[2]!.reject(new Error('stale clipboard failure')),
  );
  await expect(page.getByRole('alert')).toHaveCount(0);
  await page.getByRole('button', { name: '挂载复制控件' }).click();
  await expect(copy).toBeEnabled();
  await copy.click();
  await page.evaluate(() =>
    window.componentClipboard[3]!.reject(new Error('current clipboard failure')),
  );
  await expect(page.getByRole('alert')).toContainText('复制失败，请选择内容后手动复制');
  await expect(copy).toBeEnabled();
});

test('success toast expiry pauses for focus and hover, and dismissal restores focus', async ({
  page,
}) => {
  await page.clock.install();
  await page.goto(fixtureURL);
  const trigger = page.getByRole('button', { name: '显示成功通知', exact: true });
  const notice = page.getByRole('status').filter({ hasText: '测试成功通知' });
  await trigger.click();
  await notice.getByRole('button', { name: '关闭通知' }).focus();
  await page.clock.runFor(6000);
  await expect(notice).toBeVisible();
  await notice.getByRole('button', { name: '关闭通知' }).press('Enter');
  await expect(notice).toHaveCount(0);
  await expect(trigger).toBeFocused();

  await trigger.click();
  await notice.hover();
  await page.clock.runFor(6000);
  await expect(notice).toBeVisible();
  await page.mouse.move(0, 0);
  await page.clock.runFor(5000);
  await expect(notice).toHaveCount(0);
  const errorTrigger = page.getByRole('button', { name: '显示错误通知', exact: true });
  await errorTrigger.click();
  const error = page.getByRole('alert').filter({ hasText: '测试错误通知' });
  await page.clock.runFor(10_000);
  await expect(error).toBeVisible();
  await error.getByRole('button', { name: '关闭通知' }).focus();
  await error.getByRole('button', { name: '关闭通知' }).press('Enter');
  await expect(error).toHaveCount(0);
  await expect(errorTrigger).toBeFocused();
});

test('tabs expose actual panels and preserve native keyboard activation and focus', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  const tabs = page.getByRole('tablist', { name: '事件内容' });
  const overview = tabs.getByRole('tab', { name: '概览' });
  const properties = tabs.getByRole('tab', { name: '属性' });
  const disabled = tabs.getByRole('tab', { name: '停用页' });
  await expect(overview).toHaveAttribute('aria-selected', 'true');
  await expect(disabled).toBeDisabled();
  await expect(page.getByRole('tabpanel')).toHaveCount(1);
  await expect(page.getByRole('tabpanel', { name: '概览' })).toHaveText('事件概览内容');
  await overview.focus();
  await overview.press('ArrowRight');
  await expect(properties).toBeFocused();
  await expect(properties).toHaveAttribute('aria-selected', 'true');
  await expect(overview).toHaveAttribute('tabindex', '-1');
  await expect(page.getByRole('tabpanel', { name: '属性' })).toHaveAttribute(
    'id',
    (await properties.getAttribute('aria-controls')) as string,
  );
  await expect(page.getByLabel('面板切换次数')).toHaveText('1');
  await properties.press('ArrowLeft');
  await expect(overview).toBeFocused();
  await overview.press('End');
  await expect(properties).toBeFocused();
  await properties.press('Home');
  await expect(overview).toBeFocused();
  await overview.press('ArrowLeft');
  await expect(properties).toBeFocused();
  await expect(page.getByLabel('面板切换次数')).toHaveText('5');
  await properties.press('Enter');
  await expect(page.getByLabel('面板切换次数')).toHaveText('6');
  await properties.press('Space');
  await expect(page.getByLabel('面板切换次数')).toHaveText('7');
  await page.getByRole('textbox', { name: '面板备注' }).fill('保留编辑');
  await overview.click();
  await properties.click();
  await expect(page.getByRole('textbox', { name: '面板备注' })).toHaveValue('保留编辑');
  await page.setViewportSize({ width: 360, height: 800 });
  await expect(tabs).toBeVisible();
  expect(await tabs.evaluate((node) => node.getBoundingClientRect().right)).toBeLessThanOrEqual(
    360,
  );
});

test('unsaved guard keeps edits recoverable and rejects navigation during pending work', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  const input = page.getByRole('textbox', { name: '未保存内容' });
  const leave = page.getByRole('button', { name: '离开编辑', exact: true });
  const dialog = page.getByRole('dialog', { name: '放弃未保存的修改？' });
  await input.fill('保留草稿');
  expect(
    await page.evaluate(
      () => !window.dispatchEvent(new Event('beforeunload', { cancelable: true })),
    ),
  ).toBe(true);
  await leave.click();
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: '继续编辑' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
  await expect(leave).toBeFocused();
  await expect(input).toHaveValue('保留草稿');
  await expect(page.getByLabel('离开次数')).toHaveText('0');
  await page.getByRole('button', { name: '请求全局离开' }).click();
  await dialog.getByRole('button', { name: '放弃修改' }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByLabel('离开次数')).toHaveText('1');
  await expect(input).toHaveValue('');
  expect(
    await page.evaluate(() =>
      window.dispatchEvent(new Event('beforeunload', { cancelable: true })),
    ),
  ).toBe(true);
  await input.fill('路由草稿');
  const next = page.getByRole('link', { name: '切换测试页面' });
  await next.click();
  await expect(dialog).toBeVisible();
  await expect(page.getByLabel('测试路径')).toHaveText('/');
  await dialog.getByRole('button', { name: '继续编辑' }).click();
  await expect(next).toBeFocused();
  await expect(input).toHaveValue('路由草稿');
  await next.click();
  await dialog.getByRole('button', { name: '放弃修改' }).click();
  await expect(page.getByLabel('测试路径')).toHaveText('/other');
  await expect(input).toHaveValue('');
  await page.getByRole('button', { name: '开始测试操作' }).click();
  await page.getByRole('link', { name: '返回测试页面' }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByLabel('测试路径')).toHaveText('/other');
  await leave.click();
  await page.getByRole('button', { name: '请求全局离开' }).click();
  await expect(page.getByLabel('离开次数')).toHaveText('1');
  await page.getByRole('button', { name: '完成测试操作' }).click();
  await page.getByRole('link', { name: '返回测试页面' }).click();
  await expect(page.getByLabel('测试路径')).toHaveText('/');
});

test('required fields announce recoverable errors and keep focus and error styling while hovered', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const field = page.getByRole('textbox', { name: '必填名称' });
  await page.getByRole('button', { name: '提交必填表单' }).click();
  await expect(page.getByLabel('校验成功次数')).toHaveText('0');
  await expect(field).toBeFocused();
  await expect(field).toHaveAttribute('aria-invalid', 'true');
  await expect(field).toHaveAccessibleDescription('名称用于区分查询。 请填写名称');
  await field.hover();
  expect(
    await field.evaluate((node) => {
      const style = getComputedStyle(node);
      return [style.borderTopColor, style.outlineColor, style.outlineStyle];
    }),
  ).toEqual(['rgb(180, 35, 53)', 'rgb(180, 35, 53)', 'solid']);
  await field.fill('可恢复的查询');
  await expect(field).not.toHaveAttribute('aria-invalid', 'true');
  await expect(field).toHaveAccessibleDescription('名称用于区分查询。');
  await field.press('Enter');
  await expect(page.getByLabel('校验成功次数')).toHaveText('1');
});

test('async buttons restore their own focus without taking a newer editing target', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  const begin = page.getByRole('button', { name: '执行异步操作', exact: true });
  const finish = page.getByRole('button', { name: '完成异步操作', exact: true });
  await begin.click();
  await expect(begin).toBeDisabled();
  await expect(begin).toHaveAttribute('aria-busy', 'true');
  await expect(begin).toHaveAccessibleName('执行异步操作');
  await finish.evaluate((node) => (node as HTMLButtonElement).click());
  await expect(begin).toBeEnabled();
  await expect(begin).toBeFocused();
  await begin.press('Space');
  await expect(begin).toBeDisabled();
  const next = page.getByRole('textbox', { name: '完成后继续编辑' });
  await next.fill('继续编辑中的值');
  await finish.evaluate((node) => (node as HTMLButtonElement).click());
  await expect(begin).toBeEnabled();
  await expect(next).toBeFocused();
  await expect(next).toHaveValue('继续编辑中的值');
});

test('dialogs isolate nested Escape, reject busy dismissal, and preserve newer or fallback focus', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  const trigger = page.getByRole('button', { name: '打开测试对话框', exact: true });
  const dialog = page.getByRole('dialog', { name: '测试对话框', exact: true });
  await trigger.click();
  await expect(dialog.getByRole('button', { name: '安全返回' })).toBeFocused();
  const titleBounds = await dialog.getByRole('heading', { name: '测试对话框' }).boundingBox();
  await page.mouse.move(titleBounds!.x + 5, titleBounds!.y + 5);
  await page.mouse.down();
  await page.mouse.move(2, 2);
  await page.mouse.up();
  await expect(dialog).toBeVisible();
  await expect(page.getByRole('textbox', { name: '完成后继续编辑' })).not.toBeFocused();
  const nestedTrigger = dialog.getByRole('button', { name: '打开嵌套确认' });
  await nestedTrigger.click();
  const nested = page.getByRole('dialog', { name: '嵌套确认', exact: true });
  await expect(nested.getByRole('button', { name: '取消嵌套确认' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(nested).not.toBeVisible();
  await expect(dialog).toBeVisible();
  await expect(nestedTrigger).toBeFocused();
  await dialog.getByRole('button', { name: '开始对话框操作' }).click();
  await expect(dialog).toHaveAttribute('aria-busy', 'true');
  await expect(dialog.getByRole('button', { name: '关闭', exact: true })).toBeDisabled();
  await page.keyboard.press('Escape');
  await page.mouse.click(2, 2);
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: '结束对话框操作' }).click();
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
  await expect(trigger).toBeFocused();
  await trigger.click();
  await dialog.getByRole('button', { name: '关闭并继续编辑' }).click();
  await expect(page.getByRole('textbox', { name: '完成后继续编辑' })).toBeFocused();
  await trigger.click();
  await page.mouse.click(2, 2);
  await expect(dialog).not.toBeVisible();
  await expect(trigger).toBeFocused();
  await trigger.click();
  await page.setViewportSize({ width: 360, height: 760 });
  const bounds = await dialog.boundingBox();
  expect(bounds).not.toBeNull();
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(360);
  for (let index = 0; index < 9; index++) {
    await page.keyboard.press('Tab');
    expect(
      await dialog.evaluate(
        (node) => node.contains(document.activeElement) || document.activeElement === document.body,
      ),
    ).toBe(true);
  }
  await page.keyboard.press('Shift+Tab');
  expect(
    await dialog.evaluate(
      (node) => node.contains(document.activeElement) || document.activeElement === document.body,
    ),
  ).toBe(true);
  await dialog.screenshot({ path: test.info().outputPath('native-dialog-mobile.png') });
  await dialog.getByRole('button', { name: '关闭并移除入口' }).click();
  await expect(trigger).toHaveCount(0);
  await expect(page.getByRole('main')).toBeFocused();
});

test('notification overflow preserves a hovered or focused message', async ({ page }) => {
  await page.goto(fixtureURL);
  await page.getByRole('button', { name: '显示成功通知', exact: true }).click();
  const notice = page.getByRole('status').filter({ hasText: '测试成功通知' });
  await notice.hover();
  await page
    .getByRole('button', { name: '批量显示通知' })
    .evaluate((node) => (node as HTMLButtonElement).click());
  await expect(notice).toBeVisible();
  await expect(page.locator('.evia-toast')).toHaveCount(5);
  await notice.getByRole('button', { name: '关闭通知' }).focus();
  await page.mouse.move(0, 0);
  await page
    .getByRole('button', { name: '批量显示通知' })
    .evaluate((node) => (node as HTMLButtonElement).click());
  await expect(notice.getByRole('button', { name: '关闭通知' })).toBeFocused();
  await expect(notice).toBeVisible();
  await expect(page.locator('.evia-toast')).toHaveCount(5);
});

test('saved charts retain every metric, distinguish categories, and render gaps, time bounds and actual bars', async ({
  page,
}) => {
  await page.goto(fixtureURL);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const fixture = page.getByRole('region', { name: '保存分析图表组件' });
  const chart = fixture.getByRole('img');
  const read = async () => {
    await fixture.getByRole('button', { name: '读取图表状态' }).click();
    await expect(fixture.getByLabel('图表实际配置')).not.toHaveText('');
    return JSON.parse((await fixture.getByLabel('图表实际配置').textContent()) || '{}') as {
      series: {
        type: string;
        name: string;
        data: unknown[];
        connectNulls: boolean;
        yAxisIndex?: number;
      }[];
      xAxis: { type: string; min?: number; max?: number; data?: string[] }[];
      yAxis: { name?: string; min?: number; data?: string[]; inverse?: boolean }[];
      legend: { data: string[] }[];
    };
  };
  await expect(chart.locator('svg')).toHaveCount(1);
  await expect(chart).toHaveAccessibleName(
    /折线图，事件数，渠道缺失（次），平均金额，渠道 null，Asia\/Shanghai/,
  );
  const from = Date.UTC(2026, 10, 9),
    hour = 3600000;
  const line = await read();
  expect(line.series.map((series) => series.type)).toEqual(['line', 'line']);
  expect(line.legend[0]?.data).toEqual(['事件数，渠道缺失（次）', '平均金额，渠道 null']);
  expect(line.xAxis[0]).toMatchObject({ type: 'time', min: from, max: from + 5 * hour });
  expect(line.series[0]?.data).toEqual([
    [from + hour, 2],
    [from + 2 * hour, 4],
    [from + 3 * hour, null],
    [from + 4 * hour, 6],
  ]);
  expect(line.series[1]?.data).toEqual([
    [from + hour, 1],
    [from + 2 * hour, null],
    [from + 3 * hour, null],
    [from + 4 * hour, null],
  ]);
  expect(line.series.every((series) => series.connectNulls === false)).toBe(true);
  expect(line.series.map((series) => series.yAxisIndex)).toEqual([0, 1]);

  await fixture.getByRole('button', { name: '显示分组柱状图' }).click();
  await expect(chart).toHaveAccessibleName(/柱状图/);
  const bar = await read();
  expect(bar.series.map((series) => series.type)).toEqual(['bar', 'bar']);
  expect(bar.xAxis[0]).toMatchObject({
    type: 'category',
    data: ['渠道缺失', '渠道 null', '渠道为空字符串'],
  });
  expect(bar.series.map((series) => series.data)).toEqual([
    [2, null, 4],
    [-1, 0, 3],
  ]);
  expect(bar.yAxis[1]?.min).toBeUndefined();

  await fixture.getByRole('button', { name: '显示局部首桶' }).click();
  const partial = await read();
  expect(partial.xAxis[0]).toMatchObject({
    type: 'time',
    min: from + 11 * hour,
    max: from + 12 * hour,
  });
  expect(partial.series[0]?.data).toEqual([[from + 11 * hour, 3]]);
  await expect(chart).toHaveAccessibleDescription(/首个时间桶按本次范围起点绘制/);
  await expect(
    fixture.getByRole('cell', { name: new Date(from).toISOString(), exact: true }),
  ).toBeVisible();
  await chart.screenshot({ path: test.info().outputPath('saved-chart-partial-bucket.png') });

  await fixture.getByRole('button', { name: '显示漏斗图' }).click();
  const funnel = await read();
  expect(funnel.series.map((series) => series.type)).toEqual(['bar', 'bar']);
  expect(funnel.yAxis[0]).toMatchObject({
    inverse: true,
    data: ['渠道缺失', '渠道 null', '渠道为空字符串'],
  });
  await page.setViewportSize({ width: 360, height: 800 });
  await expect(chart.locator('svg')).toHaveCount(1);
  expect(await chart.evaluate((node) => node.getBoundingClientRect().right)).toBeLessThanOrEqual(
    360,
  );
  await chart.screenshot({ path: test.info().outputPath('saved-chart-mobile.png') });
  await fixture.getByRole('button', { name: '卸载图表' }).click();
  await expect(chart).toHaveCount(0);
  await fixture.getByRole('button', { name: '挂载图表' }).click();
  await expect(chart.locator('svg')).toHaveCount(1);
});
