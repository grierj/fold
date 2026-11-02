// PR #225 follow-up: real target geometry, heading order, answer placement,
// and archive/Undo at the affected widths. Run after verify-ui on verify-fold.
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { measure } from '../web/e2e/ui-measure.mjs';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const url = process.env.VERIFY_URL;
const evidence = process.env.VERIFY_EVIDENCE;
const database = new URL(process.env.VERIFY_DATABASE_URL || 'postgres://invalid/invalid').pathname.slice(1);
if (!url || !evidence || !/^fold_verify_[a-z0-9_]+$/.test(database) || database.includes('_e2e')) throw new Error('Use verify-fold env.');
const { seed } = JSON.parse(await readFile(`${evidence}/ui/results.json`, 'utf8'));
const dir = `${evidence}/pr225`;
await mkdir(dir, { recursive: true });
const browser = await chromium.launch();
const sessions = new Map();
const results = [];
async function session(who) {
  if (!sessions.has(who)) {
    const context = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise' });
    const page = await context.newPage();
    if (who) {
      await page.goto(url + '/login');
      await page.getByLabel('Email', { exact: true }).fill(`verify-${who}@example.com`);
      await page.getByLabel('Password', { exact: true }).fill('password1234');
      await page.getByRole('button', { name: 'Log in', exact: true }).click();
      await page.waitForURL((u) => u.pathname !== '/login');
    }
    sessions.set(who, page);
  }
  return sessions.get(who);
}
async function api(page, method, path, data) {
  const { csrfToken } = await (await page.request.get(url + '/api/v1/auth/csrf')).json();
  const r = await page.request.fetch(url + '/api/v1' + path, { method, data, headers: { 'X-CSRF-Token': csrfToken } });
  assert.ok(r.ok(), `${method} ${path}: ${r.status()} ${await r.text()}`);
  return r.json();
}
async function open(page, route, width) {
  await page.setViewportSize({ width, height: 900 });
  await page.goto(url + route);
  await page.locator('main').waitFor();
  await page.waitForLoadState('networkidle');
}
async function target(locator) {
  await expect(locator).toBeVisible();
  const r = await locator.boundingBox();
  assert.ok(r.width >= 44 && r.height >= 44, `${await locator.textContent()}: ${r.width}×${r.height}`);
  return r;
}
async function capture(page, id, width, checks = {}) {
  const m = await page.evaluate(measure);
  const wcag = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  const headings = await new AxeBuilder({ page }).withRules(['heading-order']).analyze();
  const axe = [...wcag.violations, ...headings.violations].filter((v) => ['moderate', 'serious', 'critical'].includes(v.impact));
  const fails = [m.overflow > 0 && m.overflow, m.cutOff.length && m.cutOff, m.clipped.length && m.clipped, m.under24.length && m.under24, axe.length && axe.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) }))].filter(Boolean);
  await page.screenshot({ path: `${dir}/${id}-${width}.png`, fullPage: true, animations: 'disabled' });
  results.push({ id, width, fails, checks });
  assert.deepEqual(fails, [], `${id}@${width}`);
  console.log(`ok ${id}@${width}`);
}
try {
  const admin = await session('admin');
  const member = await session('member');
  const leader = await session('leader');
  const anon = await session(null);
  const { users } = await api(admin, 'GET', '/admin/users');
  if (!users.some((u) => u.email === 'verify-outsider@example.com')) await api(admin, 'POST', '/admin/users', { name: 'Review Nonmember', email: 'verify-outsider@example.com', password: 'password1234', ageBand: 'adult' });
  const outsider = await session('outsider');
  // Ordinary role changes produce stored notices, then restore membership.
  const userId = users.find((u) => u.email === 'verify-member@example.com').id;
  for (const role of ['leader', 'member']) await api(admin, 'POST', `/admin/groups/${seed.groupId}/members`, { userId, role });
  const existing = (await api(admin, 'GET', `/groups/${seed.groupId}/events`)).events;
  const day = new Date(Date.now() + 2 * 864e5).toISOString().slice(0, 10);
  const events = [];
  for (const [index, status] of ['going', 'maybe', 'waitlisted', 'declined', null].entries()) {
    const previous = existing.find((e) => e.title === `Review date ${index + 1}`);
    const { event } = previous ? { event: { id: previous.eventId } } : await api(admin, 'POST', `/groups/${seed.groupId}/events`, {
      title: `Review date ${index + 1}`, startsLocal: `${day}T${String(10 + index).padStart(2, '0')}:00`,
      durationMinutes: 60, tzid: 'America/Boise', visibility: 'public',
      ...(status === 'waitlisted' ? { capacity: 1, waitlistEnabled: true } : {}),
    });
    if (status === 'waitlisted') await api(leader, 'PUT', `/events/${event.id}/registrations`, { date: day, status: 'going' });
    if (status) await api(member, 'PUT', `/events/${event.id}/registrations`, { date: day, status: status === 'waitlisted' ? 'going' : status });
    events.push(event.id);
  }
  for (const width of [390, 768, 1280]) {
    for (const [route, name] of [['/login', 'Request an account'], ['/join', 'Log in'], ['/calendar', 'Log in']]) {
      await open(anon, route, width);
      const geometry = await target(anon.locator('main').getByRole('link', { name, exact: true }));
      await capture(anon, route.slice(1) + '-signed-out', width, { target: geometry });
    }
    for (const [who, page] of [['public', anon], ['member', member], ['leader', leader], ['nonmember', outsider], ['admin', admin]]) {
      await open(page, `/events/${events[0]}`, width);
      const share = await target(page.getByRole('button', { name: 'Share', exact: true }));
      const names = page.getByRole('button', { name: "Show who's coming", exact: true });
      if (await names.count()) await names.click();
      await capture(page, `single-event-${who}`, width, { share });
    }
    await open(member, `/calendar?date=${day}`, width);
    const positions = [];
    for (const [index, label] of ['Going', 'Maybe', 'Waitlisted', "Can't go", 'Not answered'].entries()) {
      const row = member.getByRole('link').filter({ hasText: `Review date ${index + 1}` });
      const status = row.getByLabel(label, { exact: true });
      const placement = await status.evaluate((el) => {
        const status = el.getBoundingClientRect();
        const time = el.previousElementSibling.getBoundingClientRect();
        return { statusTop: status.top, timeBottom: time.bottom, left: status.left, timeLeft: time.left };
      });
      assert.ok(placement.statusTop >= placement.timeBottom && Math.abs(placement.left - placement.timeLeft) < 1, label);
      positions.push({ label, ...placement });
    }
    await capture(member, 'mixed-agenda', width, { positions });
    await open(member, `/calendar?date=${day}&view=week`, width);
    await capture(member, 'mixed-week', width);
    await open(member, '/', width);
    await capture(member, 'home', width);
    await open(member, '/notifications', width);
    const archives = member.getByRole('button', { name: /^Archive:/ });
    const count = await archives.count();
    assert.ok(count > 0, 'A stored member notice must be present');
    const control = archives.first();
    const geometry = await target(control);
    const unobstructed = await control.evaluate((el) => {
      const r = el.getBoundingClientRect();
      return [[6, r.height / 2], [r.width - 6, r.height / 2], [r.width / 2, 6], [r.width / 2, r.height - 6]].every(([x, y]) => el.contains(document.elementFromPoint(r.x + x, r.y + y)));
    });
    assert.ok(unobstructed, 'Archive target edges must not be covered by the row link');
    await capture(member, 'alerts', width, { archive: geometry, unobstructed });
    await member.mouse.click(geometry.x + geometry.width - 6, geometry.y + geometry.height / 2);
    await expect(archives).toHaveCount(count - 1);
    assert.equal(new URL(member.url()).pathname, '/notifications');
    await capture(member, 'alerts-archived', width, { remaining: count - 1 });
    await member.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(archives).toHaveCount(count);
    await capture(member, 'alerts-restored', width, { restored: count });
  }
} finally {
  await browser.close();
  await writeFile(`${dir}/results.json`, JSON.stringify({ url, seed, results }, null, 2) + '\n');
}
