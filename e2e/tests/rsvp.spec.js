const { test, expect, useApp, login, click, openMail } = require('../lib/app');

useApp();

// The seeded invite is UID 2 (second `seed:` entry, e2e/fixtures/invite.eml).
test('accepting a calendar invite sends the reply and files a copy in Sent', async ({ page, rt }) => {
  await login(page, rt);
  await openMail(page, 'Test', 'INBOX', 2);

  await expect(page.locator('#rsvp-accept-2')).toBeVisible();
  await click(page, '#rsvp-accept-2');

  await expect(page.locator('.toast-success', { hasText: 'sent to olive@example.com' }))
    .toBeVisible({ timeout: 15_000 });

  await expect.poll(async () => {
    const r = await page.request.get(`${rt.baseURL}/api/mails?account=Test&mailbox=Sent`);
    if (!r.ok()) return '';
    return JSON.stringify(await r.json());
  }, { timeout: 20_000 }).toContain('Accepted: Q3 planning');
});
