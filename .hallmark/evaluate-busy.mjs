// Busy-church browsing experiment; Alerts uses its normal seen acknowledgement.
// API responses are real. Local timings are not production performance claims.
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { measure } from '../web/e2e/ui-measure.mjs';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium } = require('playwright'); const { expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const url = process.env.VERIFY_URL, evidence = process.env.VERIFY_EVIDENCE;
if (!url || new URL(url).hostname !== '127.0.0.1' || !evidence || !process.env.VERIFY_DATABASE_URL?.includes('fold_verify_')) throw new Error('Use verify-fold env');
const { seed } = JSON.parse(await readFile(`${evidence}/ui/results.json`, 'utf8'));
const manifest = JSON.parse(await readFile(`${evidence}/busy-church/manifest.json`, 'utf8'));
const dir = `${evidence}/busy-church`; await mkdir(dir, { recursive: true });
const browser = await chromium.launch(); const results = [], tasks = [], sessions = new Map();
const cases = [
  ['visitor-home', null, '/'], ['member-home', 'member', '/'],
  ['my-groups', 'member', '/my-groups'],
  ['group-events', 'member', `/groups/${seed.groupId}`],
  ['group-roster', 'admin', `/groups/${seed.groupId}`, async (p) => p.getByRole('tab', { name: 'Members', exact: true }).click()],
  ['series-dates', 'member', `/events/${manifest.seriesId}`],
  ['attendee-roster', 'admin', `/events/${manifest.attendeeEvent.id}`, async (p) => { await p.getByRole('button', { name: `More for ${manifest.attendeeEvent.date}`, exact: true }).click(); await p.getByRole('menuitem', { name: 'Manage sign-ups', exact: true }).click(); await expect(p.locator('[aria-label="Manage sign-ups"] > ul > li')).toHaveCount(manifest.attendeeCount); }],
  ['calendar-agenda', 'member', '/calendar'], ['calendar-week', 'member', `/calendar?view=week&date=${manifest.attendeeEvent.date}`], ['calendar-month', 'member', `/calendar?view=month&date=${manifest.attendeeEvent.date}`, async (p) => { const day = p.locator(`[data-day="${manifest.attendeeEvent.date}"]:visible`); await day.click(); await expect(day).toHaveAttribute('aria-pressed', 'true'); }],
  ['notifications', 'member', '/notifications'],
  ['admin-users', 'admin', '/admin?tab=users'], ['admin-groups', 'admin', '/admin?tab=groups'],
  ['messages', 'admin', '/messages'], ['admin-rooms', 'admin', '/admin?tab=rooms'],
];
const only = (process.env.BUSY_CASES || '').split(',').filter(Boolean);
async function capture(page, id, width) {
  await page.waitForLoadState('networkidle'); await page.evaluate(() => document.fonts.ready);
  const metrics = await page.evaluate(() => {
    const visible = (e) => e.getBoundingClientRect().width && e.getBoundingClientRect().height;
    const top = (e) => e ? Math.round(e.getBoundingClientRect().top + scrollY) : null;
    const resources = performance.getEntriesByType('resource').filter((r) => r.name.includes('/api/v1/'));
    return {
      pageHeight: document.documentElement.scrollHeight, nodeCount: document.querySelectorAll('*').length,
      listItems: document.querySelectorAll('main li').length, articles: document.querySelectorAll('main article').length,
      tableRows: document.querySelectorAll('main tbody tr').length,
      controls: [...document.querySelectorAll('main a,main button,main input,main select')].filter(visible).length,
      groupRows: document.querySelectorAll('#groups .directory-results > li').length,
      firstGroupY: top(document.querySelector('#groups .directory-results > li')),
      agendaDates: document.querySelectorAll('[aria-labelledby="coming-up-heading"] li').length,
      manageRosterRows: document.querySelectorAll('[aria-label="Manage sign-ups"] > ul > li').length,
      loadButtons: [...document.querySelectorAll('main button')].filter((e) => /more|next/i.test(e.textContent) && visible(e)).map((e) => ({ text: e.textContent, y: top(e) })),
      requests: resources.map((r) => ({ path: new URL(r.name).pathname, bytes: r.decodedBodySize, duration: Math.round(r.duration * 10) / 10 })),
    };
  });
  await page.screenshot({ path: `${dir}/${id}-${width}.png`, animations: 'disabled' });
  await page.screenshot({ path: `${dir}/${id}-${width}-full.png`, fullPage: true, animations: 'disabled' });
  const m = await page.evaluate(measure);
  const { violations } = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  const axe = violations.filter((v) => ['moderate', 'serious', 'critical'].includes(v.impact)).map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }));
  const fails = [m.overflow > 0 && { overflow: m.overflow }, m.cutOff.length && { cutOff: m.cutOff }, m.clipped.length && { clipped: m.clipped }, m.under24.length && { under24: m.under24 }, axe.length && { axe }].filter(Boolean);
  results.push({ id, width, metrics, fails }); console.log(`${fails.length ? 'FAIL' : 'ok'} ${id}@${width}: ${metrics.pageHeight}px, ${metrics.nodeCount} nodes${fails.length ? ' ' + JSON.stringify(fails) : ''}`);
}
try {
  for (const [id, role, route, act] of cases.filter(([id]) => !only.length || only.includes(id))) {
    if (!sessions.has(role)) {
      const context = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise' }); const page = await context.newPage(); page.setDefaultTimeout(20000);
      if (role) {
        await page.goto(url + '/login'); await page.getByLabel('Email', { exact: true }).fill(`verify-${role}@example.com`); await page.getByLabel('Password', { exact: true }).fill('password1234');
        await page.getByRole('button', { name: 'Log in', exact: true }).click(); await page.waitForURL((u) => u.pathname !== '/login');
      }
      sessions.set(role, page);
    }
    const page = sessions.get(role);
    for (const width of [390, 768, 1280]) {
      await page.setViewportSize({ width, height: 900 }); await page.goto(url + route); await page.waitForLoadState('networkidle');
      if (act) { await act(page); await page.waitForLoadState('networkidle'); await page.evaluate(() => scrollTo(0, 0)); }
      await capture(page, id, width);
      if (id.endsWith('-home')) {
        await expect(page.locator('#groups .directory-results > li')).toHaveCount(12);
        if (role) await expect(page.locator('[aria-labelledby="coming-up-heading"] li')).toHaveCount(4);
        await page.getByRole('button', { name: 'Browse groups', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Groups', exact: true })).toBeFocused();
        const y = await page.locator('#groups .directory-results > li').first().evaluate((e) => e.getBoundingClientRect().top);
        expect(y).toBeLessThan(350); tasks.push({ id: id + '-jump', width, firstGroupViewportY: y, pass: true });
        await capture(page, id + '-jump', width);
      }
      if (id === 'admin-users') {
        await expect(page.locator('tbody tr')).toHaveCount(100);
        await page.getByRole('searchbox').fill('busy-097@example.com'); await expect(page.locator('tbody tr')).toHaveCount(1);
        tasks.push({ id: 'find-user-by-email', width, pass: true }); await capture(page, 'admin-users-search', width);
      }
      if (id === 'group-events') {
        let pages = 1;
        while (await page.getByRole('button', { name: 'Load more dates', exact: true }).count()) {
          if (pages++ > 10) throw new Error('Unbounded date pagination');
          const response = page.waitForResponse((r) => r.url().includes(`/api/v1/groups/${seed.groupId}?`) && r.request().method() === 'GET');
          await page.getByRole('button', { name: 'Load more dates', exact: true }).click();
          const body = await (await response).json();
          await expect(page.getByRole('button', { name: 'Loading dates…', exact: true })).toHaveCount(0);
          if (body.hasMore) await expect(page.getByRole('button', { name: 'Load more dates', exact: true })).toBeVisible();
          else await expect(page.getByRole('button', { name: 'Load more dates', exact: true })).toHaveCount(0);
        }
        tasks.push({ id: 'load-all-group-dates', width, pages, renderedSeries: await page.locator('main article').count(), pass: true });
        await page.evaluate(() => scrollTo(0, 0)); await capture(page, 'group-events-loaded', width);
      }
      if (id === 'notifications') {
        const before = await page.locator('main li').count();
        const more = page.getByRole('button', { name: 'Show more', exact: true });
        if (await more.count()) {
          await more.click(); await expect.poll(() => page.locator('main li').count()).toBeGreaterThan(before);
          const after = await page.locator('main li').count(); tasks.push({ id: 'more-notifications', width, before, after, pass: true });
        }
      }
    }
  }
} finally {
  await browser.close(); await writeFile(`${dir}/results${only.length ? '-targeted' : ''}.json`, JSON.stringify({ url, results, tasks }, null, 2) + '\n');
}
if (results.some((r) => r.fails.length)) process.exitCode = 1;
