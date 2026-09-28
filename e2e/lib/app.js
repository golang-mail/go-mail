// Per-spec app lifecycle, the extended `test`, and shared UI helpers.
//
// Every spec imports `test`/`expect` from here and calls `useApp()` once at
// file scope: that boots a fresh go-mail (own config dir, mail store and
// ports) before the file's tests and stops it afterwards, so files never
// share INBOX state or depend on ordering.
//
// The extended `page` fixture also turns real clicks into dispatched DOM
// events. Headless Chromium in this environment never fires
// requestAnimationFrame, so Playwright's actionability wait (element
// stability) in page.click/check hangs until timeout. Dispatched events still
// bubble to the delegated data-action listener exactly like a user click.
const base = require('@playwright/test');
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

const harness = require('./harness');
const { readRuntime, waitForPort, stopProcess, FIXTURES_DIR } = harness;

const { expect } = base;

const APP_USER = 'admin';
const APP_PASS = 'e2e-secret-pass';
const MAIL_USER = 'victim@test.local';
const MAIL_PASS = 'mail-pass';
const ACCOUNT = 'Test';

const BOOT_ATTEMPTS = 3;
// Go's net package wording for a lost port race, on the web or mail ports.
const BIND_FAILURE = /address already in use/;

// Seeds are appended in order: xss.eml is UID 1, invite.eml UID 2.
const SEEDS = ['xss.eml', 'invite.eml'].map((f) => path.join(FIXTURES_DIR, f));

function writeConfig(cfgDir, mailStore, ports) {
  fs.writeFileSync(path.join(cfgDir, 'server.yml'),
    `username: ${APP_USER}\n` +
    `password: ${APP_PASS}\n` +
    `address: 127.0.0.1\n` +
    `port: ${ports.web}\n` +
    `db: memory\n` +
    `log_level: ${process.env.E2E_LOG_LEVEL || 'error'}\n` +
    `filter_period: 1h\n` +
    `metrics: false\n` +
    `test:\n` +
    `  address: 127.0.0.1\n` +
    `  port: ${ports.imap}\n` +
    `  smtp_port: ${ports.smtp}\n` +
    `  username: ${MAIL_USER}\n` +
    `  password: ${MAIL_PASS}\n` +
    `  tls: none\n` +
    `  storage_dir: ${mailStore}\n` +
    `  seed:\n` +
    SEEDS.map((f) => `    - file: ${f}\n`).join(''));

  fs.writeFileSync(path.join(cfgDir, 'accounts.yml'),
    `outgoing_servers:\n` +
    `  test-out:\n` +
    `    server: 127.0.0.1\n` +
    `    port: "${ports.smtp}"\n` +
    `    username: ${MAIL_USER}\n` +
    `    password: ${MAIL_PASS}\n` +
    `    tls: none\n` +
    `accounts:\n` +
    `  - name: ${ACCOUNT}\n` +
    `    server: 127.0.0.1\n` +
    `    port: "${ports.imap}"\n` +
    `    username: ${MAIL_USER}\n` +
    `    password: ${MAIL_PASS}\n` +
    `    tls: none\n` +
    `    outgoing: test-out\n` +
    `    sent_mailbox: Sent\n` +
    `    from_addresses:\n` +
    `      - email: ${MAIL_USER}\n` +
    `        name: Victim\n`);

  fs.writeFileSync(path.join(cfgDir, 'filters.yml'), 'filters: []\n');
}

// Resolve when the web port accepts connections; reject as soon as the app
// exits instead, so a lost port race is noticed without waiting out the timeout.
function waitForWeb(child, port, timeoutMs) {
  const ac = new AbortController();
  const onExit = (code, signal) => ac.abort(new Error(`app exited before listening (code ${code}, signal ${signal})`));
  child.once('exit', onExit);
  return waitForPort(port, '127.0.0.1', timeoutMs, ac.signal).finally(() => child.off('exit', onExit));
}

// Reject `work` early if the app exits meanwhile. Whoever won a port race
// still accepts TCP, so a port being up does not prove it is our process.
function unlessExited(child, work) {
  let onExit;
  const exited = new Promise((_, reject) => {
    onExit = (code, signal) => reject(new Error(`app exited (code ${code}, signal ${signal})`));
    child.once('exit', onExit);
  });
  return Promise.race([work, exited]).finally(() => child.off('exit', onExit));
}

// Log in once and poll /api/sync-status until the initial sync is complete,
// so the first spec does not race a busy server.
async function waitForReady(baseURL, timeoutMs) {
  const loginResp = await fetch(`${baseURL}/api/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ username: APP_USER, password: APP_PASS }),
    redirect: 'manual',
    signal: AbortSignal.timeout(5_000),
  });
  const cookie = (loginResp.headers.get('set-cookie') || '').split(';')[0];
  if (!cookie.startsWith('mail_session_id=')) {
    throw new Error(`login during boot failed (status ${loginResp.status})`);
  }

  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const r = await fetch(`${baseURL}/api/sync-status`, { headers: { Cookie: cookie }, signal: AbortSignal.timeout(5_000) });
    const { status } = await r.json();
    if (status === 'ready') return;
    if (Date.now() > deadline) throw new Error(`sync not ready within ${timeoutMs}ms (last: ${status})`);
    await new Promise((res) => setTimeout(res, 200));
  }
}

function readFrom(file, offset) {
  if (!fs.existsSync(file)) return '(no log)';
  return fs.readFileSync(file, 'utf8').slice(offset);
}

// Boot one app instance under rootDir. Ports are reserved in JS before the
// app binds them, so a lost race is retried with fresh ports and a clean
// config/mail store; anything else fails with app.log attached.
async function bootApp() {
  const { binPath, rootDir } = readRuntime();
  const workDir = fs.mkdtempSync(path.join(rootDir, 'app-'));
  const cfgDir = path.join(workDir, '.go-mail');
  const mailStore = path.join(workDir, 'mailstore');
  const logPath = path.join(workDir, 'app.log');

  for (let attempt = 1; attempt <= BOOT_ATTEMPTS; attempt++) {
    for (const dir of [cfgDir, mailStore]) {
      fs.rmSync(dir, { recursive: true, force: true });
      fs.mkdirSync(dir, { recursive: true });
    }
    const [web, imap, smtp] = await Promise.all([harness.freePort(), harness.freePort(), harness.freePort()]);
    const ports = { web, imap, smtp };
    writeConfig(cfgDir, mailStore, ports);

    fs.appendFileSync(logPath, `--- boot attempt ${attempt}/${BOOT_ATTEMPTS} (web ${web}, imap ${imap}, smtp ${smtp}) ---\n`);
    const logOffset = fs.statSync(logPath).size;
    // Own process group so stop can kill the whole tree; HOME redirection makes
    // the app read cfgDir instead of the user's real ~/.go-mail.
    const logFd = fs.openSync(logPath, 'a');
    const child = spawn(binPath, [], {
      cwd: workDir,
      env: { ...process.env, HOME: workDir },
      detached: true,
      stdio: ['ignore', logFd, logFd],
    });
    child.unref();
    fs.closeSync(logFd);

    const baseURL = `http://127.0.0.1:${web}`;
    try {
      await waitForWeb(child, web, 20_000);
      await unlessExited(child, waitForReady(baseURL, 30_000));
    } catch (err) {
      await stopProcess(child.pid);
      const log = readFrom(logPath, logOffset);
      if (BIND_FAILURE.test(log) && attempt < BOOT_ATTEMPTS) continue;
      throw new Error(`app did not become ready (attempt ${attempt}/${BOOT_ATTEMPTS}): ${err.message}\n--- ${logPath} ---\n${readFrom(logPath, 0)}`);
    }

    const app = {
      pid: child.pid,
      workDir,
      logPath,
      rt: { baseURL, username: APP_USER, password: APP_PASS, account: ACCOUNT, mailUser: MAIL_USER },
    };
    // Record for global teardown, which sweeps whatever a crashed worker left.
    fs.mkdirSync(path.join(rootDir, 'apps'), { recursive: true });
    fs.writeFileSync(path.join(rootDir, 'apps', `${child.pid}.json`), JSON.stringify(app, null, 2));
    return app;
  }
  throw new Error('unreachable');
}

async function stopApp(app) {
  if (!app) return;
  await stopProcess(app.pid);
  const { rootDir } = readRuntime();
  fs.rmSync(path.join(rootDir, 'apps', `${app.pid}.json`), { force: true });
  if (!process.env.E2E_KEEP) fs.rmSync(app.workDir, { recursive: true, force: true });
}

// The app booted for the spec file currently running in this worker.
let current = null;

// Call once at file scope: boots before the file's tests, stops after them.
function useApp() {
  test.beforeAll(async () => {
    test.setTimeout(90_000);
    current = await bootApp();
  });
  test.afterAll(async () => {
    const app = current;
    current = null;
    await stopApp(app);
  });
}

// --- no-real-click enforcement -------------------------------------------

const patchedPages = new WeakSet();
let locatorPatched = false;

async function dispatchClick(locator) {
  await locator.dispatchEvent('click');
}

async function setChecked(locator, want) {
  if ((await locator.isChecked()) !== want) await locator.dispatchEvent('click');
  if (want) await expect(locator).toBeChecked();
  else await expect(locator).not.toBeChecked();
}

// Locators are created from a shared prototype, so patch it once and route
// only locators belonging to a fixture-provided page through dispatchEvent.
function patchLocatorPrototype(sample) {
  if (locatorPatched) return;
  locatorPatched = true;
  const proto = Object.getPrototypeOf(sample);
  const orig = { click: proto.click, dblclick: proto.dblclick, check: proto.check, uncheck: proto.uncheck };
  const owned = (loc) => { try { return patchedPages.has(loc.page()); } catch { return false; } };
  proto.click = function (...args) { return owned(this) ? dispatchClick(this) : orig.click.apply(this, args); };
  proto.dblclick = function (...args) { return owned(this) ? this.dispatchEvent('dblclick') : orig.dblclick.apply(this, args); };
  proto.check = function (...args) { return owned(this) ? setChecked(this, true) : orig.check.apply(this, args); };
  proto.uncheck = function (...args) { return owned(this) ? setChecked(this, false) : orig.uncheck.apply(this, args); };
}

function patchPage(page) {
  patchedPages.add(page);
  patchLocatorPrototype(page.locator('body'));
  page.click = (sel) => dispatchClick(page.locator(sel).first());
  page.dblclick = (sel) => page.locator(sel).first().dispatchEvent('dblclick');
  page.check = (sel) => setChecked(page.locator(sel).first(), true);
  page.uncheck = (sel) => setChecked(page.locator(sel).first(), false);
}

const test = base.test.extend({
  // Runtime info (baseURL, credentials, account) of this file's app.
  rt: async ({}, use) => {
    if (!current) throw new Error('no app booted for this spec file: call useApp() at file scope');
    await use(current.rt);
  },
  // Block Google Fonts so the suite is hermetic and the cold load does not
  // reflow mid-test.
  context: async ({ context }, use) => {
    await context.route(/fonts\.(googleapis|gstatic)\.com/, (route) => route.abort());
    await use(context);
  },
  page: async ({ page }, use) => {
    patchPage(page);
    await use(page);
  },
});

// --- UI helpers ------------------------------------------------------------

// Log in and wait for the first sync so the account tabs are rendered. The
// form is submitted directly: the cold-load paint leaves the button unstable.
async function login(page, rt) {
  await page.goto(`${rt.baseURL}/login`);
  await page.fill('#username', rt.username);
  await page.fill('#password', rt.password);
  await page.evaluate(() => document.querySelector('form').submit());
  await expect
    .poll(() => new URL(page.url()).pathname, { timeout: 20_000 })
    .not.toBe('/login');
  await expect.poll(async () => {
    const r = await page.request.get(`${rt.baseURL}/api/sync-status`);
    return (await r.json()).status;
  }, { timeout: 20_000 }).toBe('ready');
  await page.waitForSelector('.account-tablinks', { timeout: 10_000 });
}

// Dispatch a real DOM click on the first match (see the header note).
function click(page, selector) {
  return page.locator(selector).first().dispatchEvent('click');
}

// Deep-link straight to a message the way the SPA's hash router does.
async function openMail(page, account, mailbox, uid) {
  await page.evaluate((h) => {
    window.location.hash = h;
    if (typeof renderFromHash === 'function') renderFromHash();
  }, `account=${account}&mailbox=${mailbox}&uid=${uid}`);
  await expect(page.locator('#mail-body')).toBeVisible({ timeout: 10_000 });
}

module.exports = { test, expect, useApp, bootApp, stopApp, login, click, openMail };
