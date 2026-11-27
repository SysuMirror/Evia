import { expect, test } from '@playwright/test';
import { loginAsE2EUser } from './auth-session';

test.use({ trace: 'off', screenshot: 'off', video: 'off', reducedMotion: 'reduce' });

const username = process.env.EVIA_E2E_USERNAME;
const password = process.env.EVIA_E2E_PASSWORD;
const projectID = process.env.EVIA_E2E_REPLAY_PROJECT;

test('owner discovers replay streams and renders a sandboxed snapshot', async ({ page }) => {
  test.skip(
    process.env.EVIA_E2E_FULL_STACK !== '1' || !username || !password || !projectID,
    'Requires a real full-stack replay fixture project.',
  );
  test.setTimeout(120_000);
  const responses: number[] = [];
  page.on('response', (response) => {
    if (response.url().includes('/content/')) responses.push(response.status());
  });
  await loginAsE2EUser(page, username, password);
  await page.goto(`/projects/${projectID}/session-replay?environment=development`);
  await expect(page.getByRole('heading', { name: '会话回放', exact: true })).toBeVisible();
  const stream = page.locator('section[aria-label="回放会话流"] button.project-link').first();
  await expect(stream).toBeVisible();
  await stream.click();
  await expect(page.locator('section[aria-label="会话内容项"] tbody tr').first()).toBeVisible();
  await page.getByRole('button', { name: '加载安全快照预览', exact: true }).click();
  const frame = page.locator('iframe[title="会话回放 DOM 快照"]');
  await expect(frame).toBeVisible();
  await expect(frame.contentFrame().locator('body')).not.toHaveText('');
  const progress = page.getByLabel('回放进度');
  if (await progress.count()) {
    await progress.fill('5');
    await expect(page.getByText('回放记录应用失败')).toHaveCount(0);
  }
  expect(responses.length).toBeGreaterThanOrEqual(4);
  expect(new Set(responses)).toEqual(new Set([200]));
  await expect(frame).toHaveAttribute('sandbox', 'allow-same-origin');
  expect((await frame.getAttribute('sandbox'))?.includes('allow-scripts')).toBe(false);
  await expect(frame).toHaveAttribute('referrerpolicy', 'no-referrer');
});
