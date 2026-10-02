#!/usr/bin/env node
//
// Browser assertions for the sign-in page. Three of them need a browser and
// cannot be had from a Go handler test.
//
// #1160: a deployment configured for SSO must OFFER the flow, and a refused
// sign-on must come BACK to the sign-in page saying so, rather than to a page of
// raw JSON. A Go handler test pins the rendered bytes, which is necessary and
// not sufficient: the defect was that a real user, on a correctly configured SSO
// deployment, saw a username and password form and no way in. They typed their
// IdP credentials into it, got "Invalid credentials", and produced no audit
// record at all, because no OIDC request was ever made.
//
// #1191: a break-glass password login on top of a live SSO session must REPLACE
// the identity. This is the one that is browser rules all the way down. A script
// cannot overwrite an HttpOnly cookie, so while the sign-in page set the session
// cookie with document.cookie, the token a password login had just been issued
// never reached the jar: the browser kept sending the SSO cookie, the UI kept
// showing the SSO identity, and the server logged a 200. Nothing below the
// browser can observe that, so it is asserted here, through a real SSO login
// against a real IdP handshake and a real form submit with no sign-out in
// between.
//
// #1191, second half: the session cookie must be HttpOnly on BOTH paths. The
// JWT-only arm proves it where it was never true at all, by asserting that a
// signed-in page cannot see the session token in document.cookie.
//
// Same reasoning as ui-smoke.js, which exists because the React layer once
// shipped broken and only a browser caught it.
//
// Usage (see sso-login-page.sh, which stands up the fake IdP and the servers):
//   LEOFLOW_URL=http://localhost:18080 node test/e2e/sso-login-page.js
//   LEOFLOW_URL=http://localhost:18081 LEOFLOW_EXPECT_SSO=0 node test/e2e/sso-login-page.js
const { chromium } = require('playwright-core');

const URL_BASE = process.env.LEOFLOW_URL || 'http://localhost:18080';
const EXPECT_SSO = process.env.LEOFLOW_EXPECT_SSO !== '0';
// Where the flow is supposed to end up. Asserting only that the browser left the
// sign-in page is satisfied by a 500 from /api/v2/auth/oidc/login, whose URL does
// not contain "/api/v2/auth/login" either.
const IDP_ORIGIN = process.env.LEOFLOW_IDP_ORIGIN || 'https://localhost:18443';
// The two identities. They must differ: "the identity changed" is the assertion.
const SSO_EMAIL = process.env.LEOFLOW_SSO_EMAIL || 'sso-user@example.com';
const BREAK_GLASS_EMAIL = process.env.LEOFLOW_BREAK_GLASS_EMAIL || 'breakglass@example.com';
const BREAK_GLASS_PASSWORD = process.env.LEOFLOW_BREAK_GLASS_PASSWORD || '';

function cachedChromium() {
  const fs = require('fs');
  for (const dir of [
    `${process.env.HOME}/Library/Caches/ms-playwright`,
    `${process.env.HOME}/.cache/ms-playwright`,
  ]) {
    if (!fs.existsSync(dir)) continue;
    for (const entry of fs.readdirSync(dir)) {
      if (!entry.startsWith('chromium')) continue;
      for (const candidate of [
        `${dir}/${entry}/chrome-linux/chrome`,
        `${dir}/${entry}/chrome-mac/Chromium.app/Contents/MacOS/Chromium`,
      ]) {
        if (fs.existsSync(candidate)) return candidate;
      }
    }
  }
  return undefined;
}

const fail = (msg) => { console.error(`FAIL: ${msg}`); process.exitCode = 1; };

// whoami asks the server who the browser's cookie jar says it is. It is the
// only honest way to read the session: the cookie is HttpOnly, so the page
// cannot look at it, and that is the point.
async function whoami(page) {
  return page.evaluate(async () => {
    const r = await fetch('/ui/auth/me', { credentials: 'same-origin' });
    if (!r.ok) return { status: r.status, username: null };
    const body = await r.json().catch(() => ({}));
    return { status: r.status, username: body.username || null };
  });
}

// signInWithPassword drives the real form the way a person does: type, submit,
// wait for the page to leave the sign-in URL. No cookie is set by hand, which
// is the whole subject of the test.
async function signInWithPassword(page, email, password) {
  await page.goto(`${URL_BASE}/api/v2/auth/login`, { waitUntil: 'domcontentloaded' });
  await page.fill('input[name="username"]', email);
  await page.fill('input[name="password"]', password);
  await page.click('button[type="submit"]');
  await page.waitForFunction(
    () => !window.location.pathname.startsWith('/api/v2/auth/login'),
    null, { timeout: 15000 },
  ).catch(() => {});
  await page.waitForLoadState('domcontentloaded').catch(() => {});
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: cachedChromium() });
  // The fake IdP holds a throwaway CA's cert. Without this the redirect ends on
  // Chromium's interstitial, whose URL is still the IdP's, so the assertions
  // below would pass on a handshake that never completed.
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();
  // Keep the URL beside the error. A completed login lands on the SPA, whose
  // own crashes are ui-smoke.js's subject, not this file's; failing here for one
  // would turn an unrelated React regression into a mystery failure in the SSO
  // job. Only errors raised on the sign-in page itself are this test's.
  const errors = [];
  page.on('pageerror', (e) => errors.push({ url: page.url(), msg: String(e) }));

  await page.goto(`${URL_BASE}/api/v2/auth/login?next=/dags`, { waitUntil: 'domcontentloaded' });

  const sso = page.locator('a.sso');
  const ssoCount = await sso.count();

  if (EXPECT_SSO) {
    if (ssoCount === 0) {
      fail('the sign-in page offers no SSO control; a user configured for SSO has no route into the flow but the address bar');
    } else {
      const href = await sso.first().getAttribute('href');
      if (!href || !href.startsWith('/api/v2/auth/oidc/login')) {
        fail(`the SSO control points at ${href}, not the route that starts the flow`);
      }
      if (!href.includes('next=')) {
        fail('the SSO control drops ?next=, so an SSO login cannot return the user to the page they asked for');
      }
      // Following it must actually reach the IdP's authorization endpoint with a
      // usable request. A control that renders and goes nowhere is the same dead
      // end with extra steps, and so is one that 500s on the way.
      await sso.first().click();
      await page.waitForLoadState('domcontentloaded');
      const landed = page.url();
      if (!landed.startsWith(IDP_ORIGIN)) {
        fail(`clicking the SSO control landed on ${landed}, not the IdP at ${IDP_ORIGIN}`);
      } else {
        const q = new URL(landed).searchParams;
        // PKCE and the CSRF/replay bindings are what make the redirect a login
        // rather than a link. Losing any of them is silent from the browser.
        for (const param of ['client_id', 'redirect_uri', 'state', 'nonce', 'code_challenge']) {
          if (!q.get(param)) {
            fail(`the authorization request carries no ${param}: ${landed}`);
          }
        }
      }
    }
    // Finish the sign-on. The fake IdP's /authorize is a page with one link,
    // the way a real consent screen is a page with one button, so the step above
    // could assert the authorization request and this one can complete it.
    const cont = page.locator('#continue');
    if (await cont.count() === 0) {
      fail('the fake IdP did not offer the continue link; the rest of the flow cannot be driven');
    } else {
      await cont.click();
      await page.waitForLoadState('domcontentloaded');
      if (!page.url().startsWith(URL_BASE)) {
        fail(`completing the sign-on left the browser on ${page.url()}, not back on Dexaflow`);
      }
      const me = await whoami(page);
      if (me.username !== SSO_EMAIL) {
        fail(`after a completed SSO login /ui/auth/me says ${JSON.stringify(me)}, want ${SSO_EMAIL}`);
      }
      // The session cookie the callback set must be invisible to scripts. If it
      // is not, the deployment is one XSS away from handing out live sessions.
      const jar = await page.evaluate(() => document.cookie);
      if (jar.includes('_token')) {
        fail(`the SSO session cookie is readable from document.cookie: ${jar}`);
      }
    }

    // break_glass_emails exists so named local logins still work when the IdP is
    // down. The form has to stay.
    await page.goto(`${URL_BASE}/api/v2/auth/login`, { waitUntil: 'domcontentloaded' });
    if (await page.locator('input[name="password"]').count() === 0) {
      fail('the password form is gone; break-glass accounts have no way in when the IdP is unreachable');
    }

    // #1191. A break-glass login WITHOUT signing out first. This is the exact
    // sequence from the production report: SSO is live, the operator is not sure
    // it works, and they use the escape hatch. The old code handed the token to
    // the page, which wrote document.cookie, which the browser dropped on the
    // floor because an HttpOnly _token was already there. The server answered
    // 200 and the UI went on showing the SSO user, which reads as "the escape
    // hatch did not open" at the worst possible moment.
    if (!BREAK_GLASS_PASSWORD) {
      fail('no break-glass password was provided; the #1191 assertion cannot run');
    } else {
      const before = await whoami(page);
      if (before.username !== SSO_EMAIL) {
        fail(`expected to still be signed in as ${SSO_EMAIL} before the break-glass login, got ${JSON.stringify(before)}`);
      }
      await signInWithPassword(page, BREAK_GLASS_EMAIL, BREAK_GLASS_PASSWORD);
      const after = await whoami(page);
      if (after.username !== BREAK_GLASS_EMAIL) {
        fail(`a break-glass login over a live SSO session left the identity as ${JSON.stringify(after)}, `
          + `want ${BREAK_GLASS_EMAIL}. The password login answered 200 and the browser kept the old session.`);
      }
      const jar = await page.evaluate(() => document.cookie);
      if (jar.includes('_token')) {
        fail(`the break-glass session cookie is readable from document.cookie: ${jar}`);
      }
    }

    // Sign-out has to clear the cookie the login just set. It is the same
    // name/path/attributes question in the other direction: a deletion the
    // browser does not match leaves a live session behind a redirect that says
    // the session ended.
    await page.goto(`${URL_BASE}/api/v2/auth/logout`, { waitUntil: 'domcontentloaded' });
    const afterLogout = await whoami(page);
    if (afterLogout.status !== 401) {
      fail(`after logout /ui/auth/me says ${JSON.stringify(afterLogout)}, want 401: the session outlived the sign-out`);
    }

    // A REFUSED login has to land somewhere a person can act on. This is the
    // property a Go handler test cannot assert: the old answer was a 403 with a
    // problem+json body, which is a correct API response and, to the browser that
    // is the only caller of this route, a page of raw JSON with no way back to the
    // sign-in page. Driving the callback with a state that cannot match is a real
    // rejection through the real fail-closed path.
    await page.goto(`${URL_BASE}/api/v2/auth/oidc/callback?code=nope&state=nope`, { waitUntil: 'domcontentloaded' });
    const denied = page.url();
    if (!denied.startsWith(`${URL_BASE}/api/v2/auth/login`)) {
      fail(`a refused sign-on left the browser on ${denied}, not back on the sign-in page`);
    }
    if (await page.locator('input[name="password"]').count() === 0) {
      fail(`a refused sign-on rendered something that is not the sign-in page: ${denied}`);
    }
    const banner = page.locator('.ssoerr');
    if (await banner.count() === 0) {
      fail('a refused sign-on returned the bare sign-in form, which reads as the button having done nothing');
    }
    // The reason is withheld from the browser on purpose: it is the same map of
    // the deployment someone probing it is after. Assert on the rendered page, not
    // just the URL, because the banner is the one place it could creep back in.
    const shown = `${denied} ${await page.content()}`.toLowerCase();
    for (const leak of ['state_mismatch', 'invalid_state', 'missing_state', 'tenant_not_allowed',
      'email_not_verified', 'email_domain_not_allowed', 'token_expired', 'exchange_failed']) {
      if (shown.includes(leak)) {
        fail(`the refused sign-on told the browser why: ${leak}`);
      }
    }
  } else {
    if (ssoCount > 0) {
      fail('a deployment with no OIDC flow advertises the SSO route; following it 404s');
    }
    // The unreported half of #1191, on the deployment where it was always true.
    // A JWT-only control plane never reaches the OIDC callback, so its session
    // cookie was only ever the one the page wrote with document.cookie: not
    // HttpOnly, readable by anything running on the page. Nothing about that is
    // visible in a response body, and it is why this arm signs in for real.
    if (!BREAK_GLASS_PASSWORD) {
      fail('no password was provided; the JWT-only session cookie assertion cannot run');
    } else {
      await signInWithPassword(page, BREAK_GLASS_EMAIL, BREAK_GLASS_PASSWORD);
      const me = await whoami(page);
      if (me.username !== BREAK_GLASS_EMAIL) {
        fail(`after a password login /ui/auth/me says ${JSON.stringify(me)}, want ${BREAK_GLASS_EMAIL}. `
          + 'The session cookie now comes from the response, so a login that does not authenticate means it never arrived.');
      }
      const jar = await page.evaluate(() => document.cookie);
      if (jar.includes('_token')) {
        fail(`the session token is readable from document.cookie on a JWT-only deployment: ${jar}`);
      }
    }
  }

  const signInErrors = errors.filter((e) => e.url.includes('/api/v2/auth/'));
  if (signInErrors.length) {
    fail(`uncaught errors on the sign-in page: ${signInErrors.map((e) => e.msg).join(' | ')}`);
  }
  await context.close();
  await browser.close();
  if (!process.exitCode) {
    console.log(`ok: sign-in page at ${URL_BASE} ${EXPECT_SSO ? 'offers' : 'correctly omits'} the SSO flow`);
  }
})().catch((e) => { console.error(e); process.exit(1); });
