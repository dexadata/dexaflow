#!/usr/bin/env node
//
// The API tokens screen (#1886), driven in a real browser: the "API tokens"
// entry of the Airflow UI's user menu opens the screen in the UI's frame, the
// screen lists a token created for the run with its metadata and never its
// secret, and revoking it there asks to confirm, removes the row, and stops
// the token on its next request.
//
// ui-smoke.js runs it as one of its steps. It also runs alone against any
// control plane with a signed-in admin:
//   LEOFLOW_URL=http://localhost:18080 LEOFLOW_USER=admin@dexaflow.local \
//     LEOFLOW_PASS=<pw> node test/e2e/ui-api-tokens.js
// SHOTS_DIR=<dir> saves a screenshot of the list and of the confirmation.
//
// Exit code 0 = the flow held; 1 = a step failed.

const fs = require('fs');
const path = require('path');

// withToken answers the status of a DAG list read with token alone. It uses
// Node's fetch, not page.request, which would also send the browser's session
// cookie and so authenticate even with a revoked token.
async function withToken(url, token) {
  const res = await fetch(url + '/api/v2/dags?limit=1', { headers: { Authorization: 'Bearer ' + token } });
  return res.status;
}

// apiTokensFlow runs the flow in page, a page already signed in to url as
// user. It throws on the first broken expectation.
async function apiTokensFlow(page, url, user, pass, shotsDir) {
  const auth = await (await page.request.post(url + '/auth/token',
    { data: { username: user, password: pass } })).json().catch(() => ({}));
  if (!auth.access_token) throw new Error('POST /auth/token returned no access_token');
  const bearer = { Authorization: 'Bearer ' + auth.access_token };
  const name = 'ui-smoke-' + Date.now();
  const res = await page.request.post(url + '/api/v2/apiTokens',
    { headers: bearer, data: { name, scope: 'read', expires_in_seconds: 3600 } });
  const created = await res.json().catch(() => ({}));
  if (res.status() !== 201 || !created.token) throw new Error(`creating a token answered ${res.status()}`);

  // The entry sits in the user menu (an external view of category "user").
  await page.goto(url + '/', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /^user$/i }).click({ timeout: 5000 });
  await page.getByRole('menuitem', { name: 'API tokens' }).click({ timeout: 5000 });
  await page.waitForURL(/\/plugin\/api-tokens/, { timeout: 5000 });

  const frame = page.frameLocator('iframe[title="API tokens"]');
  const row = frame.locator('tr', { hasText: name });
  await row.waitFor({ timeout: 8000 });
  const cells = await row.locator('td').allInnerTexts();
  if (cells[1] !== 'read') throw new Error(`scope cell = "${cells[1]}", want read`);
  if (cells[2] !== user) throw new Error(`owner cell = "${cells[2]}", want ${user}`);
  if (cells[5] !== 'Never') throw new Error(`last used cell = "${cells[5]}", want Never`);
  const html = await frame.locator('html').innerHTML();
  if (html.includes(created.token) || /dxf_/.test(html)) throw new Error('the screen shows a token secret');
  if (shotsDir) await page.screenshot({ path: path.join(shotsDir, 'api-tokens-list.png') });

  // Revoke asks first, and Cancel leaves the token alone.
  await row.getByRole('button', { name: 'Revoke ' + name }).click({ timeout: 5000 });
  const confirm = row.getByRole('button', { name: 'Confirm revoking ' + name });
  await confirm.waitFor({ timeout: 5000 });
  if (shotsDir) await page.screenshot({ path: path.join(shotsDir, 'api-tokens-confirm.png') });
  await row.getByRole('button', { name: 'Cancel' }).click();
  if (await confirm.count()) throw new Error('Cancel left the confirmation open');
  const still = await withToken(url, created.token);
  if (still !== 200) throw new Error(`the token after Cancel answered ${still}, want 200`);

  await row.getByRole('button', { name: 'Revoke ' + name }).click({ timeout: 5000 });
  await confirm.click({ timeout: 5000 });
  await frame.getByText('Revoked ' + name + '.').waitFor({ timeout: 5000 });
  await frame.locator('tr', { hasText: name }).waitFor({ state: 'detached', timeout: 5000 });
  if (shotsDir) await page.screenshot({ path: path.join(shotsDir, 'api-tokens-revoked.png') });
  const after = await withToken(url, created.token);
  if (after !== 401) throw new Error(`the revoked token answered ${after}, want 401`);
}

module.exports = { apiTokensFlow };

if (require.main === module) {
  const { chromium } = require('playwright-core');
  const url = process.env.LEOFLOW_URL || 'http://localhost:18080';
  const user = process.env.LEOFLOW_USER || 'admin@dexaflow.local';
  const pass = process.env.LEOFLOW_PASS || '';
  const shots = process.env.SHOTS_DIR || '';
  (async () => {
    if (shots) fs.mkdirSync(shots, { recursive: true });
    const browser = await chromium.launch({ headless: true });
    const page = await (await browser.newContext({ viewport: { width: 1280, height: 720 } })).newPage();
    const errs = [];
    page.on('pageerror', (e) => errs.push(e.message));
    try {
      await page.goto(url, { waitUntil: 'networkidle' });
      await page.fill('input[name=username]', user);
      await page.fill('input[name=password]', pass);
      await page.click('button[type=submit]');
      await page.waitForLoadState('networkidle');
      await apiTokensFlow(page, url, user, pass, shots);
      if (errs.length) throw new Error('page errors: ' + errs.join(' | '));
      console.log('API TOKENS SCREEN PASSED');
      await browser.close();
      process.exit(0);
    } catch (e) {
      console.error('API TOKENS SCREEN FAILED: ' + e.message.split('\n')[0]);
      if (shots) await page.screenshot({ path: path.join(shots, 'api-tokens-failure.png') }).catch(() => {});
      await browser.close();
      process.exit(1);
    }
  })();
}
