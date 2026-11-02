// Supplemental redesign evidence, using the same isolated instance and checks
// as verify-ui. The existing verification script and all tests stay unchanged.
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { measure } from '../web/e2e/ui-measure.mjs';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium } = require('playwright');
const AxeBuilder = require('@axe-core/playwright').default;
const url = process.env.VERIFY_URL;
const evidence = process.env.VERIFY_EVIDENCE;
if (!url || !evidence || !process.env.VERIFY_DATABASE_URL?.includes('fold_verify_')) throw new Error('Use verify-fold env for an isolated run.');
const { seed } = JSON.parse(await readFile(`${evidence}/ui/results.json`, 'utf8'));
const dir = process.env.VERIFY_EXTRA_DIR || `${evidence}/extra`;
const only = (process.env.UI_PAGES || '').split(',').filter(Boolean);
const widths = (process.env.UI_WIDTHS || '390,1280').split(',').map(Number);
await mkdir(dir, { recursive: true });
const cases = [
  ['calendar-agenda', 'member', '/calendar'],
  ['calendar-week', 'member', '/calendar?view=week'],
  ['calendar-month', 'member', '/calendar?view=month'],
  ['rooms', 'admin', '/calendar/rooms'],
  ['profile-requests', 'member', '/profile/requests'],
  ['profile-notifications', 'member', '/profile/notifications'],
  ['contact-requests', 'member', '/contact-requests'],
  ['add-requests', 'member', '/add-requests'],
  ['contact-history', 'admin', '/admin/contact-history'],
  ['admin-settings', 'admin', '/admin?tab=settings'],
  ['admin-groups', 'admin', '/admin?tab=groups'],
  ['admin-invite', 'admin', '/admin?tab=invite'],
  ['notifications-setup', 'member', '/notifications/setup'],
  ['forgot-password', null, '/forgot-password'],
  ['verify-email-invalid', null, '/verify-email'],
  ['guest-confirm-invalid', null, '/guest/confirm'],
  ['guest-manage-invalid', null, '/guest/manage/invalid'],
  ['consent-invalid', null, '/guardian-consent'],
  ['cancel-email-invalid', null, '/cancel-email-change'],
  ['not-found', null, '/not-a-fold-page'],
  ['group-menu', 'leader', `/groups/${seed.groupId}`, async (p) => p.getByRole('button', { name: 'More group actions' }).click()],
  ['group-about', 'member', `/groups/${seed.groupId}`, async (p) => p.getByRole('tab', { name: 'About', exact: true }).click()],
  ['group-members', 'member', `/groups/${seed.groupId}`, async (p) => p.getByRole('tab', { name: 'Members', exact: true }).click()],
  ['event-edit', 'leader', `/events/${seed.eventId}`, async (p) => p.getByRole('button', { name: 'Edit series', exact: true }).click()],
  ['date-edit', 'leader', `/events/${seed.eventId}`, async (p) => {
    await p.getByRole('button', { name: /^More for / }).first().click();
    await p.getByRole('menuitem', { name: 'Edit this date', exact: true }).click();
  }],
  ['group-filter', 'member', '/', async (p) => { await p.getByRole('button', { name: 'Filters', exact: true }).click(); await p.getByRole('button', { name: 'music', exact: true }).click(); await p.getByRole('group', { name: 'Selected group filters' }).waitFor(); }],
  ['rsvp-dialog', 'member', `/events/${seed.eventId}`, async (p) => {
    await p.getByRole('button', { name: 'Maybe', exact: true }).first().click();
    await p.getByRole('dialog').waitFor();
  }],
  ['rsvp-selected', 'member', `/events/${seed.eventId}`, async (p) => {
    const going = p.getByRole('button', { name: 'Going', exact: true }).first();
    if (await going.getAttribute('aria-pressed') !== 'true') {
      await going.click();
      await p.getByRole('button', { name: 'Just this date', exact: true }).click();
      await p.waitForFunction(() => Array.from(document.querySelectorAll('button')).some((b) => b.textContent.trim() === 'Going' && b.getAttribute('aria-pressed') === 'true'));
    }
  }],
];
const browser = await chromium.launch();
const sessions = new Map();
const results = [];
try {
  for (const [id, role, route, act] of cases.filter(([id]) => !only.length || only.includes(id))) {
    const key = role ?? 'anon';
    if (!sessions.has(key)) {
      const context = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise' });
      const page = await context.newPage();
      page.setDefaultTimeout(15000);
      if (role) {
        await page.goto(url + '/login');
        await page.getByLabel('Email', { exact: true }).fill(`verify-${role}@example.com`);
        await page.getByLabel('Password', { exact: true }).fill('password1234');
        await page.getByRole('button', { name: 'Log in', exact: true }).click();
        await page.waitForURL((u) => u.pathname !== '/login');
      }
      sessions.set(key, page);
    }
    const page = sessions.get(key);
    for (const width of widths) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(url + route);
      await page.locator('main').waitFor();
      await page.waitForLoadState('networkidle');
      if (act) { await act(page); await page.waitForLoadState('networkidle'); }
      const shot = `${id}-${width}.png`;
      await page.screenshot({ path: `${dir}/${shot}`, fullPage: true, animations: 'disabled' });
      const m = await page.evaluate(measure);
      const { violations } = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
      const axe = violations.filter((v) => ['moderate', 'serious', 'critical'].includes(v.impact)).map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }));
      const fails = [m.overflow > 0 && `overflow ${m.overflow}`, m.cutOff.length > 0 && { cutOff: m.cutOff }, m.clipped.length > 0 && { clipped: m.clipped }, m.under24.length > 0 && { under24: m.under24 }, axe.length > 0 && { axe }].filter(Boolean);
      const nativeFields = await page.locator('input[type=date], input[type=time], input[type=number]').evaluateAll((fields) => fields.map((f) => ({ type: f.type, width: f.getBoundingClientRect().width, fontSize: getComputedStyle(f).fontSize, value: f.value })));
      results.push({ id, role, route, width, shot, fails, under44: m.under44, nativeFields });
      console.log(`${fails.length ? 'FAIL' : 'ok'} ${id}@${width}${fails.length ? ' ' + JSON.stringify(fails) : ''}`);
    }
  }
} finally {
  await browser.close();
  await writeFile(`${dir}/results.json`, JSON.stringify({ url, seed, results }, null, 2) + '\n');
}
if (results.some((r) => r.fails.length)) process.exitCode = 1;
