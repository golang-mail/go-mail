const { test, expect, useApp, login, click } = require('../lib/app');

useApp();

test('labels are listed as cards with their chip, scope and storage', async ({ page, rt }) => {
  await login(page, rt);
  const name = `e2e-label-${Date.now()}`;
  const res = await page.request.post(`${rt.baseURL}/api/labels`, {
    data: { name, color: '#8e24aa', storage: 'imap', accounts: ['Test'], hidden: true, weight: 7 },
  });
  expect(res.ok(), await res.text()).toBe(true);
  const other = await page.request.post(`${rt.baseURL}/api/labels`, { data: { name: `other-${Date.now()}`, color: '#00897b' } });
  expect(other.ok(), await other.text()).toBe(true);

  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-labels"]');
  const card = page.locator('#labels-list .filter-card', { hasText: name });
  await expect(card).toBeVisible({ timeout: 10_000 });
  await expect(card.locator('.label-chip')).toHaveText(name);
  await expect(card.locator('.filter-card-scope')).toContainText('Test');
  await expect(card.locator('.filter-card-summary')).toContainText('IMAP keyword');
  await expect(card.locator('.filter-card-summary')).toContainText('hidden');
  await expect(card.locator('.filter-card-summary')).toContainText('weight 7');
  await expect(page.locator('#labels-count')).toContainText('label');

  // Search narrows the list and the count reflects it.
  await page.fill("#label-search", "other-");
  await expect(page.locator('#labels-list .filter-card')).toHaveCount(1);
  await expect(page.locator('#labels-count')).toContainText('1 of 2');
  await page.fill('#label-search', '');

  await card.dispatchEvent('click');
  await expect(page.locator('#new-label-content')).toBeVisible();
  await expect(page.locator('#label-name')).toHaveValue(name);
});

test('the plugins page shows an empty state and a count', async ({ page, rt }) => {
  await login(page, rt);
  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-plugins"]');
  await expect(page.locator('#plugins-content')).toBeVisible();
  await expect(page.locator('#plugins-list .filter-list-empty')).toBeVisible({ timeout: 10_000 });
  await expect(page.locator('#plugins-count')).toContainText('0 plugins');
});
