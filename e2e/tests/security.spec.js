const { test, expect, useApp, login, click } = require('../lib/app');
const { CANARY } = require('../lib/harness');

useApp();

test('login sets an HttpOnly session cookie that scripts cannot read', async ({ page, context, rt }) => {
  await login(page, rt);

  const cookies = await context.cookies();
  const session = cookies.find((c) => c.name === 'mail_session_id');
  expect(session, 'session cookie should be set').toBeTruthy();
  expect(session.httpOnly, 'cookie must be HttpOnly').toBe(true);
  expect(session.sameSite).toBe('Strict');

  const jsCookies = await page.evaluate(() => document.cookie);
  expect(jsCookies).not.toContain('mail_session_id');
});

test('responses carry security headers including a CSP', async ({ page, rt }) => {
  await login(page, rt);

  const resp = await page.request.get(`${rt.baseURL}/`);
  const headers = resp.headers();
  const csp = headers['content-security-policy'];
  expect(csp).toContain("default-src 'self'");
  const scriptSrc = csp.split(';').map((d) => d.trim()).find((d) => d.startsWith('script-src'));
  expect(scriptSrc).toMatch(/'nonce-[A-Za-z0-9_-]+'/);
  expect(scriptSrc).not.toContain("'unsafe-inline'");
  expect(headers['x-content-type-options']).toBe('nosniff');
  expect(headers['x-frame-options']).toBe('DENY');
});

test('the app runs without CSP violations under the nonce policy', async ({ page, rt }) => {
  const violations = [];
  page.on('console', (msg) => {
    if (/Content Security Policy|Refused to/.test(msg.text())) violations.push(msg.text());
  });
  page.on('pageerror', (err) => violations.push(String(err)));

  await login(page, rt);
  // Exercise the converted inline handlers through real DOM click events.
  await click(page, '.compose-btn');
  await expect(page.locator('#compose-content')).toBeVisible();
  await click(page, '[data-action="discard-compose"]');
  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-filters"]');
  await expect(page.locator('#filters-content')).toBeVisible();
  await click(page, '#mail-content [data-action="show-mails"], .btn-icon[data-action="show-mails"]');
  await expect(page.locator('#mail-list-scroll')).toBeVisible();

  expect(violations, violations.join('\n')).toEqual([]);
});

test('metrics endpoint is disabled unless configured', async ({ page, rt }) => {
  await login(page, rt);
  const resp = await page.request.get(`${rt.baseURL}/metrics`);
  expect(resp.status()).toBe(404);
});

// The seeded payload is UID 1 (first `seed:` entry, e2e/fixtures/xss.eml).
test('a malicious plain-text email body renders inert', async ({ page, rt }) => {
  await login(page, rt);

  // Deep-link to the seeded message; renderFromHash routes straight to showMail.
  await page.evaluate((h) => {
    window.__xssFired = undefined;
    window.location.hash = h;
    if (typeof renderFromHash === 'function') renderFromHash();
  }, 'account=Test&mailbox=INBOX&uid=1');

  const body = page.locator('#mail-body');
  await expect(body).toContainText(CANARY, { timeout: 10_000 });

  // The payload must render as literal text, not as live DOM, and must not run.
  expect(await body.locator('img').count()).toBe(0);
  expect(await body.locator('script').count()).toBe(0);
  expect(await page.evaluate(() => window.__xssFired)).toBeUndefined();
  // The raw markup should be visible as escaped text.
  await expect(body).toContainText('<img');
  await expect(body).toContainText('<script>');
});
