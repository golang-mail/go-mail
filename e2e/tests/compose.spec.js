const { test, expect, useApp, login, click } = require('../lib/app');

useApp();

test('a message sent from compose is delivered and shows up without a reload', async ({ page, rt }) => {
  await login(page, rt);

  const subject = `e2e compose ${Date.now()}`;
  await click(page, '.compose-btn');
  await expect(page.locator('#compose-content')).toBeVisible();
  await page.fill('#compose-to', rt.mailUser);
  await page.fill('#compose-subject', subject);
  await page.locator('#compose-body-html').evaluate((el) => { el.textContent = 'hello from the browser test'; });
  await click(page, '#compose-send');

  // The test SMTP server delivers into INBOX; IMAP IDLE pushes the change and
  // the server-sent event refreshes the list, so no reload or 60s poll is needed.
  await expect(page.locator('#mail-list-scroll .mail-row', { hasText: subject }))
    .toBeVisible({ timeout: 20_000 });

  // The Sent copy is filed in the background.
  await expect.poll(async () => {
    const r = await page.request.get(`${rt.baseURL}/api/mails?account=Test&mailbox=Sent`);
    if (!r.ok()) return 0;
    const body = await r.json();
    return (body.conversations || []).filter((c) => JSON.stringify(c).includes(subject)).length;
  }, { timeout: 20_000 }).toBe(1);
});
