// Verify the new directory against the 50-group fixture, before the busy seed.
import { createRequire } from 'node:module';
import { mkdir, writeFile } from 'node:fs/promises';
import { measure } from '../web/e2e/ui-measure.mjs';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium } = require('playwright');
const { expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const url = process.env.VERIFY_URL, evidence = process.env.VERIFY_EVIDENCE;
if (!url || new URL(url).hostname !== '127.0.0.1' || !evidence || !process.env.VERIFY_DATABASE_URL?.includes('fold_verify_')) throw new Error('Use verify-fold env');
const dir = `${evidence}/directory-new`; await mkdir(dir, { recursive: true });
const browser = await chromium.launch(); const results = [];
async function shot(page, id, width) {
  await page.waitForLoadState('networkidle');
  const metrics = await page.evaluate(() => {
    const list = [...document.querySelectorAll('#groups .directory-results > li')];
    return { pageHeight: document.documentElement.scrollHeight, firstGroupY: list[0]?.getBoundingClientRect().top + scrollY,
      firstGroupViewportY: list[0]?.getBoundingClientRect().top, rows: list.length, scrollY,
      columns: list.length > 1 && list[0].getBoundingClientRect().top === list[1].getBoundingClientRect().top ? 2 : 1,
      toolbarHeight: document.querySelector('.directory-toolbar').getBoundingClientRect().height,
      filterExpanded: document.querySelector('.directory-toolbar button').getAttribute('aria-expanded') };
  });
  await page.screenshot({ path: `${dir}/${id}-${width}.png`, animations: 'disabled' });
  await page.screenshot({ path: `${dir}/${id}-${width}-full.png`, fullPage: true, animations: 'disabled' });
  const m = await page.evaluate(measure);
  const { violations } = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  const axe = violations.filter((v) => ['moderate', 'serious', 'critical'].includes(v.impact)).map((v) => v.id);
  const fails = [m.overflow > 0 && m.overflow, m.cutOff.length && m.cutOff, m.clipped.length && m.clipped, m.under24.length && m.under24, axe.length && axe].filter(Boolean);
  results.push({ id, width, metrics, fails }); console.log(`${fails.length ? 'FAIL' : 'ok'} ${id}@${width}`); expect(fails).toEqual([]);
}
try {
  for (const member of [false, true]) {
    const context = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise' }); const page = await context.newPage();
    if (member) {
      await page.goto(url + '/login'); await page.getByLabel('Email', { exact: true }).fill('verify-member@example.com'); await page.getByLabel('Password', { exact: true }).fill('password1234');
      await page.getByRole('button', { name: 'Log in', exact: true }).click(); await page.waitForURL((u) => u.pathname !== '/login');
    }
    for (const width of [390, 768, 1280]) {
      await page.setViewportSize({ width, height: 900 }); await page.goto(url);
      const cards = page.locator('#groups .directory-results > li');
      await expect(cards).toHaveCount(12); await expect(page.getByRole('status').filter({ hasText: 'Showing 12 of 50 groups' })).toBeVisible();
      await shot(page, member ? 'member-top' : 'visitor-top', width);
      await page.getByRole('button', { name: 'Browse groups', exact: true }).click();
      await expect(page.getByRole('heading', { name: 'Groups', exact: true })).toBeFocused();
      await shot(page, member ? 'member-directory' : 'visitor-directory', width);
      expect(results.at(-1).metrics.firstGroupViewportY).toBeLessThan(600);
      expect(results.at(-1).metrics.columns).toBe(width >= 1024 ? 2 : 1);
      await page.getByRole('button', { name: 'Filters', exact: true }).click();
      await shot(page, member ? 'member-filters' : 'visitor-filters', width);
      await page.getByRole('button', { name: 'bible-study', exact: true }).click();
      await expect(cards).toHaveCount(9);
      await page.getByRole('button', { name: 'Done with filters', exact: true }).click();
      await expect(page.getByRole('searchbox')).toBeFocused();
      await shot(page, member ? 'member-filtered' : 'visitor-filtered', width);
      await page.getByRole('button', { name: 'Clear', exact: true }).click(); await expect(cards).toHaveCount(12);
      const more = page.getByRole('button', { name: 'Show more groups', exact: true });
      await more.scrollIntoViewIfNeeded(); const before = await page.evaluate(() => scrollY);
      await more.click(); await expect(cards).toHaveCount(24);
      expect(Math.abs(await page.evaluate(() => scrollY) - before)).toBeLessThan(3);
      await shot(page, member ? 'member-more' : 'visitor-more', width);
      await cards.nth(19).scrollIntoViewIfNeeded();
      // Open the sticky disclosure from deep in the results, not just at its
      // natural position. Its controls must come back into the viewport.
      await page.getByRole('button', { name: 'Filters', exact: true }).click();
      const tagY = await page.getByRole('button', { name: 'bible-study', exact: true }).evaluate((e) => e.getBoundingClientRect().top);
      expect(tagY).toBeGreaterThan(64); expect(tagY).toBeLessThan(800);
      await page.getByRole('button', { name: 'Done with filters', exact: true }).click();
      await cards.nth(19).scrollIntoViewIfNeeded();
      const returnRow = await cards.nth(19).getAttribute('id');
      await cards.nth(19).locator('a').focus();
      const returnY = await page.evaluate(() => scrollY);
      await page.keyboard.press('Enter'); await page.waitForURL((u) => /^\/groups\/\d+$/.test(u.pathname));
      await page.goBack(); await expect(cards).toHaveCount(24);
      await expect(page.locator(`#${returnRow} a`)).toBeFocused();
      expect(Math.abs(await page.evaluate(() => scrollY) - returnY)).toBeLessThan(3);
      await page.getByRole('searchbox').fill('Lakeside'); await expect(cards).toHaveCount(1);
      await cards.first().locator('a').click(); await page.waitForURL((u) => /^\/groups\/\d+$/.test(u.pathname));
      await page.goBack(); await expect(page.getByRole('searchbox')).toHaveValue('Lakeside'); await expect(cards).toHaveCount(1);
      await shot(page, member ? 'member-search-back' : 'visitor-search-back', width);
    }
    await context.close();
  }
} finally { await browser.close(); await writeFile(`${dir}/results.json`, JSON.stringify({ url, results }, null, 2) + '\n'); }
