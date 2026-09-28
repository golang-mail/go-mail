const { test, expect, useApp, login, click } = require('../lib/app');

useApp();

test('a filter created in the editor is listed and persisted', async ({ page, rt }) => {
  await login(page, rt);

  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-filters"]');
  await expect(page.locator('#filters-content')).toBeVisible();
  await click(page, '#new-filter-btn');
  await expect(page.locator('#new-filter-content')).toBeVisible();

  const name = `e2e filter ${Date.now()}`;
  await page.fill('#filter-name', name);

  // The editor opens with one condition and one action row; fill those.
  const cond = page.locator('#filter-conditions .filter-condition-row').first();
  await cond.locator('select').first().selectOption('subject');
  const opSelect = cond.locator('select').filter({ has: page.locator('option[value="contains"]') }).first();
  await opSelect.selectOption('contains');
  await cond.locator('input[type="text"]:visible').last().fill('e2e-subject');
  await page.locator('#filter-actions .filter-action-row select').first().selectOption('mark_as_read');

  // A row added with the button must start on a real field, otherwise saving
  // sends an empty field name that the server rejects.
  await click(page, '#add-condition-btn');
  const added = page.locator('#filter-conditions .filter-condition-row').last();
  expect(await added.locator('select').first().inputValue()).not.toBe('');
  await added.locator('.filter-row-remove').dispatchEvent('click');
  await expect(page.locator('#filter-conditions .filter-condition-row')).toHaveCount(1);

  const savePromise = page.waitForResponse((r) => r.url().includes('/api/filters') && r.request().method() === 'POST');
  await click(page, '#save-filter');
  const saveResp = await savePromise;
  const detail = saveResp.status() === 201 ? '' : await saveResp.text();
  expect(saveResp.status(), detail).toBe(201);

  await expect(page.locator('#filters-list')).toContainText(name, { timeout: 10_000 });

  const res = await page.request.get(`${rt.baseURL}/api/filters`);
  expect(res.ok()).toBe(true);
  const filters = await res.json();
  const saved = filters.find((f) => f.name === name);
  expect(saved, 'filter must be persisted through the API').toBeTruthy();
  expect(saved.field).toBe('subject');
  expect(saved.op).toBe('contains');
  expect(saved.value).toBe('e2e-subject');
  expect(saved.action).toBe('mark_as_read');
});

test('the filters list shows summary cards that expand to the full condition list', async ({ page, rt }) => {
  await login(page, rt);

  // A filter with many conditions must render as a one-line summary, not a wall of rows.
  const name = `e2e many ${Date.now()}`;
  const conditions = Array.from({ length: 5 }, (_, i) => ({ field: 'from', operator: 'equals', value: `sender${i}@example.test` }));
  const res = await page.request.post(`${rt.baseURL}/api/filters`, {
    data: { name, accounts: [], mailboxes: ['INBOX'], conditions_type: 'or', conditions, actions: [{ type: 'move', value: 'Junk' }, { type: 'mark_as_read', value: '' }] },
  });
  expect(res.status()).toBe(201);

  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-filters"]');
  const card = page.locator('#filters-list .filter-card', { hasText: name });
  await expect(card).toBeVisible({ timeout: 10_000 });
  await expect(card.locator('.filter-card-scope')).toContainText('all accounts');
  await expect(card.locator('.filter-card-scope')).toContainText('INBOX');
  await expect(card.locator('.filter-card-summary')).toContainText('Any of 5 conditions');
  await expect(card.locator('.filter-card-summary')).toContainText('move to Junk');
  await expect(card.locator('.filter-card-summary')).toContainText('mark as read');
  await expect(card.locator('.filter-card-details')).toBeHidden();

  // The chevron expands in place without opening the editor.
  await card.locator('[data-action="toggle-filter-card"]').dispatchEvent('click');
  await expect(card.locator('.filter-card-details')).toBeVisible();
  await expect(card.locator('.filter-card-details .filter-card-cond')).toHaveCount(5);
  await expect(page.locator('#new-filter-content')).toBeHidden();

  // The header counts what is listed.
  await expect(page.locator('#filters-count')).toContainText('filter');
});

test('a filter can be saved disabled and re-enabled from its card', async ({ page, rt }) => {
  await login(page, rt);

  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-filters"]');
  await click(page, '#new-filter-btn');
  await expect(page.locator('#new-filter-content')).toBeVisible();

  const name = `e2e disabled ${Date.now()}`;
  await page.fill('#filter-name', name);
  const cond = page.locator('#filter-conditions .filter-condition-row').first();
  await cond.locator('select').first().selectOption('subject');
  await cond.locator('input[type="text"]:visible').last().fill('e2e-disabled');
  await page.locator('#filter-actions .filter-action-row select').first().selectOption('mark_as_read');

  // Schedule section: disabled now, and an end date with removal.
  // Dispatched like the other clicks: a real click waits for animation
  // frames, which headless Chromium does not reliably produce here.
  await click(page, '#filter-disabled');
  await expect(page.locator('#filter-disabled')).toBeChecked();
  await expect(page.locator('#filter-on-expire')).toBeHidden();
  await page.fill('#filter-active-until', '2099-01-02T03:04');
  await expect(page.locator('#filter-on-expire')).toBeVisible();
  await page.selectOption('#filter-on-expire', 'remove');

  const savePromise = page.waitForResponse((r) => r.url().includes('/api/filters') && r.request().method() === 'POST');
  await click(page, '#save-filter');
  expect((await savePromise).status()).toBe(201);

  const card = page.locator('#filters-list .filter-card', { hasText: name });
  await expect(card).toBeVisible({ timeout: 10_000 });
  await expect(card.locator('.filter-chip-status')).toContainText('disabled');
  await expect(card.locator('.filter-chip-status')).toContainText('until');

  let res = await page.request.get(`${rt.baseURL}/api/filters`);
  let saved = (await res.json()).find((f) => f.name === name);
  expect(saved.filter.disabled).toBe(true);
  expect(saved.filter.on_expire).toBe('remove');
  expect(new Date(saved.filter.active_until).getUTCFullYear()).toBe(2099);

  // The card toggle flips the flag without opening the editor.
  const putPromise = page.waitForResponse((r) => r.url().includes('/api/filters/') && r.request().method() === 'PUT');
  await card.locator('[data-action="toggle-filter-enabled"]').dispatchEvent('click');
  expect((await putPromise).status()).toBe(200);
  await expect(card.locator('.filter-chip-status')).not.toContainText('disabled');

  res = await page.request.get(`${rt.baseURL}/api/filters`);
  saved = (await res.json()).find((f) => f.name === name);
  expect(saved.filter.disabled).toBeFalsy();
  expect(saved.filter.on_expire).toBe('remove');
});
