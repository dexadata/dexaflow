#!/usr/bin/env node
//
// Headless SPA crash smoke: drives the embedded Airflow 3.2 UI of a running
// Dexaflow control plane and fails if any screen throws an uncaught error.
//
// It exists because the Go API tests cannot see the React layer: the connector
// config page shipped broken (the catalog omitted standard_fields keys the SPA
// reads unconditionally → "Cannot read properties of undefined (reading 'hidden')")
// and only a real browser caught it. This crawls the main routes plus the
// connection form (which renders EVERY connector type) and the run → task → logs →
// actions path, capturing `pageerror`/console errors per step.
//
// Run against a live Lite:
//   dexaflow lite --port 18080 --postgres managed --executor subprocess &
//   npm i playwright-core && npx playwright install chromium     # once
//   LEOFLOW_URL=http://localhost:18080 LEOFLOW_USER=admin@leoflow.local \
//     LEOFLOW_PASS=<pw> node test/e2e/ui-smoke.js
//
// Exit code 0 = no crashes; 1 = at least one screen/interaction threw.

const { chromium } = require('playwright-core');
const fs = require('fs');

const URL = process.env.LEOFLOW_URL || 'http://localhost:18080';
const USER = process.env.LEOFLOW_USER || 'admin@leoflow.local';
const PASS = process.env.LEOFLOW_PASS || '';

// Reuse a cached chromium when we can find one (macOS or Linux paths); otherwise
// return undefined and let playwright-core resolve the browser it installed.
function cachedChromium() {
  const bases = [
    `${process.env.HOME}/Library/Caches/ms-playwright`, // macOS
    `${process.env.HOME}/.cache/ms-playwright`,         // Linux
  ];
  for (const base of bases) {
    try {
      for (const d of fs.readdirSync(base).filter((x) => x.startsWith('chromium')).sort().reverse()) {
        for (const sub of ['chrome-mac/Chromium.app/Contents/MacOS/Chromium', 'chrome-linux/chrome', 'chrome-linux/headless_shell']) {
          const p = `${base}/${d}/${sub}`;
          if (fs.existsSync(p)) return p;
        }
      }
    } catch {}
  }
  return undefined;
}

const CRASH_RE = /Cannot read properties|is not a function|client-side exception|Something went wrong|Minified React error #\d/i;

// finish writes the report and exits explicitly, rather than letting the
// process end when the event loop happens to drain.
//
// The reason is symmetry, not a known leak. Both failure paths call
// process.exit, so they can never linger; the pass path fell off the end of
// the async IIFE and depended on nothing else holding the loop open. A test
// runner whose two outcomes exit by different mechanisms will eventually
// differ in behaviour, and the one that can hang is the one that succeeded —
// which is the hardest case to read from CI, because the log says it passed.
// (Searched for an actual leak first: one launch, one context, one page, all
// closed by browser.close(); every API call goes through page.request, which
// the context owns. Nothing is left open. This is insurance, not a workaround.)
//
// Flushing is the part that needs care. console.log to a pipe — which is what
// CI gives us — is asynchronous, and a bare process.exit() drops whatever has
// not reached the OS. That would truncate the result line, the one thing a
// reader needs. So the exit happens in the write callback, with an unref'd 5s
// timer as a backstop for a stdout that never drains; unref'd so the timer
// itself cannot hold the process open.
function finish(code, text, stream = process.stdout) {
  process.exitCode = code;
  setTimeout(() => process.exit(code), 5000).unref();
  stream.write(text, () => process.exit(code));
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: cachedChromium() });
  const page = await (await browser.newContext()).newPage();
  let errs = [];
  page.on('pageerror', (e) => errs.push((e.stack || e.message).split('\n').slice(0, 2).join(' | ')));
  page.on('console', (m) => {
    if (m.type() === 'error' && CRASH_RE.test(m.text())) errs.push('console: ' + m.text().split('\n')[0]);
  });

  const results = [];
  const step = async (name, fn) => {
    errs = [];
    try { await fn(); } catch (e) { errs.push('THREW: ' + e.message.split('\n')[0]); }
    await page.waitForTimeout(1200);
    const overlay = await page.getByText(CRASH_RE).first().count().catch(() => 0);
    results.push({ name, bad: !!overlay || errs.length > 0, errs: [...new Set(errs)].slice(0, 2) });
  };

  // Login (the username input has no type attr, so target by name).
  await page.goto(URL, { waitUntil: 'networkidle' }).catch(() => {});
  await page.fill('input[name=username]', USER).catch(() => {});
  await page.fill('input[name=password]', PASS).catch(() => {});
  await page.click('button[type=submit]').catch(() => {});
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);

  // 1) Every top-level screen (page load). /connections renders all connector types.
  const routes = ['/', '/dags', '/connections', '/variables', '/pools', '/providers',
    '/assets', '/config', '/plugins', '/xcoms', '/security/users', '/browse/audit_log'];
  for (const r of routes) await step('route ' + r, async () => { await page.goto(URL + r, { waitUntil: 'networkidle' }); });

  // 2) Connection form open (the bug that shipped broken).
  await step('connections: open Add form', async () => {
    await page.goto(URL + '/connections', { waitUntil: 'networkidle' });
    const add = page.getByRole('button', { name: /add connection|^add$/i }).first();
    if (await add.count()) await add.click();
  });

  // 3) Run → task instance → logs → action menu (the high-churn detail area).
  await step('dag grid: a DAG', async () => { await page.goto(URL + '/dags/hello', { waitUntil: 'networkidle' }); });
  await step('open a task cell', async () => {
    const c = page.locator('svg rect, a[href*="tasks"], [data-testid*="task-instance"]').first();
    if (await c.count()) await c.click({ timeout: 4000 });
  });
  await step('open Logs tab', async () => {
    const l = page.getByRole('tab', { name: /log/i }).or(page.getByText(/^Logs?$/)).first();
    if (await l.count()) await l.click({ timeout: 4000 });
  });
  // Logs are a historically fragile contract (empty-log regressions). Assert the
  // logs endpoint returns the structured {content:[…]} the viewer parses and that
  // at least one log line actually rendered on the page.
  await step('logs: content renders', async () => {
    // Mint our own token via the public auth endpoint (independent of where the
    // SPA stashes its session) so the logs assertion can't silently no-op.
    const auth = await (await page.request.post(URL + '/auth/token',
      { data: { username: USER, password: PASS } })).json().catch(() => ({}));
    const tok = auth.access_token;
    if (!tok) throw new Error('POST /auth/token returned no access_token (auth contract changed)');
    const run = await (await page.request.get(URL + '/api/v2/dags/hello/dagRuns?limit=1',
      { headers: { Authorization: 'Bearer ' + tok } })).json().catch(() => ({}));
    const rid = run?.dag_runs?.[0]?.dag_run_id;
    if (rid) {
      const tis = await (await page.request.get(`${URL}/api/v2/dags/hello/dagRuns/${rid}/taskInstances`,
        { headers: { Authorization: 'Bearer ' + tok } })).json().catch(() => ({}));
      const tid = tis?.task_instances?.[0]?.task_id;
      const logs = await (await page.request.get(
        `${URL}/api/v2/dags/hello/dagRuns/${rid}/taskInstances/${tid}/logs/1`,
        { headers: { Authorization: 'Bearer ' + tok, Accept: 'application/json' } })).json().catch(() => ({}));
      if (!Array.isArray(logs.content) || logs.content.length === 0) {
        throw new Error('logs endpoint returned no structured content (empty-log regression)');
      }
    }
  });
  await step('open action menu', async () => {
    const m = page.getByRole('button', { name: /clear|mark|action/i }).first();
    if (await m.count()) await m.click({ timeout: 4000 }).catch(() => {});
  });

  // The connection "Test" probe must honor the host/port/schema the form sends as
  // SEPARATE fields. Regression guard: it once built {scheme}://{host} from
  // conn_type alone, dropping port+schema, so a reachable endpoint on a non-default
  // port reported unreachable. We probe the lite server itself (guaranteed up) on
  // its real port; status:true proves the port made it into the probe URL.
  await step('connection Test honors port+schema', async () => {
    const auth = await (await page.request.post(URL + '/auth/token',
      { data: { username: USER, password: PASS } })).json().catch(() => ({}));
    const tok = auth.access_token;
    if (!tok) throw new Error('POST /auth/token returned no access_token');
    // NB: the base URL is bound to the module const `URL`, which shadows the
    // global URL constructor here — parse via Node's url module explicitly.
    const u = new (require('url').URL)(URL);
    const port = Number(u.port) || (u.protocol === 'https:' ? 443 : 80);
    const res = await (await page.request.post(URL + '/api/v2/connections/test', {
      headers: { Authorization: 'Bearer ' + tok },
      data: { connection_id: 'smoke_http', conn_type: 'http',
        host: u.hostname, port, schema: u.protocol.replace(':', '') },
    })).json().catch(() => ({}));
    if (res.status !== true) {
      throw new Error(`Test probe failed for a reachable endpoint (port/schema dropped?): ${res.message}`);
    }
  });

  // The typed trigger form (#798 UI half). params_demo declares a bare default,
  // a typed enum, and a required param; the details endpoint now serves them in
  // Airflow's {value, schema} param-dict shape, so the native trigger dialog
  // renders a generated form AND still offers the JSON editor. Prove it against
  // the real Lite (no route mocking): the details shape, both surfaces render,
  // and a real form submit creates a run carrying the expected conf.
  const paramsDag = 'params_demo';
  const mintToken = async () => {
    const auth = await (await page.request.post(URL + '/auth/token',
      { data: { username: USER, password: PASS } })).json().catch(() => ({}));
    if (!auth.access_token) throw new Error('POST /auth/token returned no access_token');
    return auth.access_token;
  };

  await step('params DAG: /details serves typed params in Airflow shape', async () => {
    const tok = await mintToken();
    const det = await (await page.request.get(`${URL}/api/v2/dags/${paramsDag}/details`,
      { headers: { Authorization: 'Bearer ' + tok } })).json().catch(() => ({}));
    const p = det.params || {};
    // The leoflow seam: declared params reshaped to {value, schema, description}.
    if (!p.region || !('value' in p.region) || !p.region.schema || p.region.schema.type !== 'string') {
      throw new Error('region not served as {value, schema:{type}} (params wiring): ' + JSON.stringify(p.region));
    }
    if (!('run_label' in p) || p.run_label.value !== null) {
      throw new Error('required param run_label should serve value:null: ' + JSON.stringify(p.run_label));
    }
  });

  await step('trigger dialog: generated form + JSON editor both render', async () => {
    await page.goto(`${URL}/dags/${paramsDag}`, { waitUntil: 'networkidle' });
    const trig = page.getByRole('button', { name: /^trigger/i }).first();
    if (!(await trig.count())) throw new Error('no Trigger button on the params DAG page');
    await trig.click({ timeout: 5000 });
    await page.waitForTimeout(1000);
    // (a) a generated form field for a declared param (the enum "Region" or the
    //     required "Run label") — proves the declared params drove the flexible form.
    if (!(await page.getByText(/Run label|Region/i).first().count())) {
      throw new Error('no generated form field rendered from declared params');
    }
    // (b) the JSON editor is still offered (form + JSON coexist — the "both" requirement).
    if (!(await page.getByText(/Configuration JSON|Advanced Options/i).first().count())) {
      throw new Error('Configuration JSON editor not offered alongside the form');
    }
  });

  await step('trigger form: submit creates a run with the expected conf', async () => {
    const tok = await mintToken();
    const label = 'smoke-' + Date.now();
    // Fill the required field (label = the param's title), then submit via the
    // dialog's Trigger button. A missing required value would 400 server-side and
    // create no run, so a created run proves the form fed a valid conf through.
    // Airflow's FlexibleForm names each generated param field input
    // `element_<param>`. For a plain-string field it renders the <label> with a
    // generated `for` id that does NOT match the input's own id
    // (id="element_run_label"), so the accessible-name association is broken and
    // getByLabel(/Run label/) resolves nothing — and a generic first-textbox
    // fallback lands on the logical-date picker (a datetime-local), leaving the
    // required run_label empty so the dialog's Trigger button stays disabled and no
    // run is ever created. Target the field by its stable, param-specific name; keep
    // the label lookup first so a future Airflow that fixes the association still works.
    let input = page.getByLabel(/Run label/i).first();
    if (!(await input.count())) input = page.locator('input[name="element_run_label"]');
    await input.fill(label);
    await page.getByRole('button', { name: /^trigger$/i }).last().click({ timeout: 5000 }).catch(() => {});
    // Poll for a run whose conf carries our unique label (order-independent).
    let created = null;
    for (let i = 0; i < 20 && !created; i++) {
      await page.waitForTimeout(1000);
      const runs = await (await page.request.get(`${URL}/api/v2/dags/${paramsDag}/dagRuns?limit=100`,
        { headers: { Authorization: 'Bearer ' + tok } })).json().catch(() => ({}));
      created = (runs.dag_runs || []).find((r) => r && r.conf && r.conf.run_label === label);
    }
    if (!created) throw new Error('form submit did not create a run carrying the submitted run_label conf');
    // The v0.4.1 backend merges declared defaults under the conf, so the untouched
    // enum defaults to "us" — the form is a faithful preview of what's enforced.
    if (created.conf.region !== 'us') {
      throw new Error('declared default region=us not materialized into conf: ' + JSON.stringify(created.conf));
    }
  });

  await browser.close();

  const failed = results.filter((r) => r.bad);
  const report = ['', '===== UI SMOKE ====='];
  for (const r of results) {
    report.push(`${r.bad ? 'FAIL' : 'ok  '}  ${r.name}`);
    r.errs.forEach((e) => report.push('        ' + e.slice(0, 200)));
  }
  report.push(
    failed.length
      ? `\nUI SMOKE FAILED: ${failed.length} screen(s) threw an uncaught error.`
      : `\nUI SMOKE PASSED: ${results.length} screens/interactions, no crashes.`,
  );
  finish(failed.length ? 1 : 0, report.join('\n') + '\n');
})().catch((e) => finish(1, `FATAL ${(e && e.stack) || String(e)}\n`, process.stderr));
