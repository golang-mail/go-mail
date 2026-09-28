const { test, expect, useApp, login, click } = require('../lib/app');

useApp();

test('a plugin card toggles enabled state in one click', async ({ page, rt }) => {
  await login(page, rt);

  // A disabled plugin never spawns, so a nonexistent command is harmless.
  const name = `e2e-plugin-${Date.now()}`;
  const created = await page.request.post(`${rt.baseURL}/api/plugins`, {
    data: { name, command: ['/nonexistent/e2e-plugin'], disabled: true },
  });
  expect(created.ok(), await created.text()).toBe(true);

  await click(page, '#settings-btn');
  await click(page, '#settings-menu [data-action="open-plugins"]');
  const card = page.locator('#plugins-list .filter-card', { hasText: name });
  await expect(card).toBeVisible({ timeout: 10_000 });
  await expect(card.locator('.plugin-status')).toHaveText('disabled');

  const toggle = card.locator('[data-action="plugin-toggle"]');
  await expect(toggle).toHaveText('Enable');
  const enablePromise = page.waitForResponse((r) => r.url().endsWith(`/api/plugins/${name}/enable`) && r.request().method() === 'POST');
  await toggle.dispatchEvent('click');
  expect((await enablePromise).status()).toBe(200);
  await expect(card.locator('[data-action="plugin-toggle"]')).toHaveText('Disable');

  let res = await page.request.get(`${rt.baseURL}/api/plugins/${name}`);
  expect((await res.json()).spec.Disabled).toBeFalsy();

  const disablePromise = page.waitForResponse((r) => r.url().endsWith(`/api/plugins/${name}/disable`) && r.request().method() === 'POST');
  await card.locator('[data-action="plugin-toggle"]').dispatchEvent('click');
  expect((await disablePromise).status()).toBe(200);
  await expect(card.locator('.plugin-status')).toHaveText('disabled');

  res = await page.request.get(`${rt.baseURL}/api/plugins/${name}`);
  expect((await res.json()).spec.Disabled).toBe(true);
});
