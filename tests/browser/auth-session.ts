import { createHash } from 'node:crypto';
import { chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, type Cookie, type Page } from '@playwright/test';

let cachedCookies: Cookie[] | null = null;

function cacheFilePath(username: string): string {
  const scope = createHash('sha256')
    .update(`${username}\0${process.env.EVIA_DATABASE_URL ?? ''}\0${process.ppid}`)
    .digest('hex')
    .slice(0, 16);
  return join(tmpdir(), `evia-e2e-session-${scope}.json`);
}

function loadCookies(username: string): Cookie[] | null {
  if (cachedCookies) return cachedCookies;
  try {
    const parsed = JSON.parse(readFileSync(cacheFilePath(username), 'utf8')) as Cookie[];
    if (Array.isArray(parsed) && parsed.length > 0) {
      cachedCookies = parsed;
      return parsed;
    }
  } catch {
    // First login in this run has no cached session file yet.
  }
  return null;
}

function saveCookies(username: string, cookies: Cookie[]) {
  cachedCookies = cookies;
  const path = cacheFilePath(username);
  writeFileSync(path, JSON.stringify(cookies), { mode: 0o600 });
  chmodSync(path, 0o600);
}

/**
 * Reuse one authenticated E2E session cookie across non-auth browser specs so
 * the full suite stays well inside the backend's 10-logins / 5-minute account
 * rate limit without weakening production login protection.
 */
export async function loginAsE2EUser(
  page: Page,
  username = process.env.EVIA_E2E_USERNAME,
  password = process.env.EVIA_E2E_PASSWORD,
): Promise<void> {
  if (!username || !password) {
    throw new Error(
      'Set EVIA_E2E_USERNAME and EVIA_E2E_PASSWORD for the dedicated local test account.',
    );
  }
  const existing = loadCookies(username);
  if (existing) {
    await page.context().addCookies(existing);
    const probe = await page.request.get('/api/v1/session');
    if (probe.status() === 200) {
      await page.goto('/projects');
      await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
      return;
    }
    cachedCookies = null;
    await page.context().clearCookies();
  }
  await page.goto('/projects');
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('账号', { exact: false }).fill(username);
  await page.getByLabel('密码', { exact: false }).fill(password);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '项目管理' })).toBeVisible();
  saveCookies(username, await page.context().cookies());
}
