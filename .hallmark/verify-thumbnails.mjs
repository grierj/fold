// Follow-up proof on a verify-fold instance, after drive-ui has seeded it.
// Existing brand PNG is an upload fixture only, not proposed group photography.
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { measure } from '../web/e2e/ui-measure.mjs';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium } = require('playwright');
const { expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const url = process.env.VERIFY_URL;
const evidence = process.env.VERIFY_EVIDENCE;
if (!url || new URL(url).hostname !== '127.0.0.1' || !evidence || !process.env.VERIFY_DATABASE_URL?.startsWith('postgres://') || !process.env.VERIFY_DATABASE_URL.includes('fold_verify_')) throw new Error('Use verify-fold env.');
const { seed } = JSON.parse(await readFile(`${evidence}/ui/results.json`, 'utf8'));
const dir = `${evidence}/thumbnails`;
await mkdir(dir, { recursive: true });
const fixture = new URL('../web/public/assets/brand/fold-icon-512.png', import.meta.url).pathname;
const hint = 'Tap or click the image to move the focal point. Faces stay centered when cropped.';
const browser = await chromium.launch();
const results = [];
const uploads = [];
async function signIn(page, role) {
  await page.goto(url + '/login');
  await page.getByLabel('Email', { exact: true }).fill(`verify-${role}@example.com`);
  await page.getByLabel('Password', { exact: true }).fill('password1234');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.waitForURL((u) => u.pathname !== '/login');
}
async function shot(page, id, width) {
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: `${dir}/${id}-${width}.png`, fullPage: true, animations: 'disabled' });
  const m = await page.evaluate(measure);
  const { violations } = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  const axe = violations.filter((v) => ['moderate', 'serious', 'critical'].includes(v.impact)).map((v) => v.id);
  const fails = [m.overflow > 0 && `overflow ${m.overflow}`, m.cutOff.length && m.cutOff, m.clipped.length && m.clipped, m.under24.length && m.under24, axe.length && axe].filter(Boolean);
  results.push({ id, width, fails, under44: m.under44 });
  expect(fails).toEqual([]);
  console.log(`ok ${id}@${width}`);
}
try {
  const adminContext = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise' });
  const admin = await adminContext.newPage();
  await signIn(admin, 'admin');
  for (const name of ['Tuesday Bible Study', 'Youth Choir']) {
    await admin.goto(url);
    await admin.getByRole('heading', { name, exact: true }).click();
    await admin.getByRole('button', { name: 'More group actions' }).click();
    const [chooser] = await Promise.all([admin.waitForEvent('filechooser'), admin.getByRole('menuitem', { name: 'Change cover photo' }).click()]);
    const response = admin.waitForResponse((r) => r.url().endsWith('/cover') && r.request().method() === 'POST');
    await chooser.setFiles(fixture);
    const uploaded = await response;
    expect(uploaded.ok()).toBe(true);
    const { imageUrl } = await uploaded.json();
    uploads.push({ name, imageUrl });
    await expect(admin.locator('.fold-cover img')).toHaveAttribute('src', imageUrl);
  }
  await admin.goto(`${url}/groups/${seed.groupId}`);
  await admin.getByRole('button', { name: 'More group actions' }).click();
  await admin.getByRole('menuitem', { name: 'Edit group', exact: true }).click();
  await expect(admin.getByText(hint, { exact: true })).toBeVisible();
  for (const width of [390, 768, 1280]) {
    await admin.setViewportSize({ width, height: 900 });
    await shot(admin, 'group-cover-editor', width);
  }
  await adminContext.close();

  const context = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise' });
  await context.addInitScript(() => {
    window.lcpEntries = [];
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) window.lcpEntries.push({ time: e.startTime, size: e.size, tag: e.element?.tagName, url: e.url });
    }).observe({ type: 'largest-contentful-paint', buffered: true });
  });
  const page = await context.newPage();
  await signIn(page, 'member');
  await page.goto(url);
  await page.getByRole('heading', { name: 'Youth Choir', exact: true }).click();
  await page.getByRole('heading', { name: 'Youth Choir', level: 1 }).waitFor();
  if (!await page.getByRole('button', { name: "You're a member", exact: true }).isVisible()) {
    await page.getByRole('button', { name: 'Join this group', exact: true }).click();
  }
  await expect(page.getByRole('button', { name: "You're a member", exact: true })).toBeVisible();
  await page.goto(`${url}/profile/details`);
  const [avatarChooser] = await Promise.all([page.waitForEvent('filechooser'), page.getByRole('button', { name: /^Choose (photo|different image)$/ }).click()]);
  await avatarChooser.setFiles(fixture);
  await expect(page.getByText('Picture saved.', { exact: true })).toBeVisible();
  await expect(page.getByText(hint, { exact: true })).toBeVisible();
  for (const width of [390, 768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await shot(page, 'profile-picture', width);
  }
  const cdp = await context.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
  let requests = [];
  cdp.on('Network.requestWillBeSent', (e) => {
    if (e.type === 'Image' && e.request.url.includes('/uploads/')) requests.push({ url: e.request.url, priority: e.request.initialPriority });
  });
  for (const [id, route] of [['home', '/'], ['my-groups', '/my-groups']]) {
    for (const width of [390, 768, 1280]) {
      requests = [];
      await page.setViewportSize({ width, height: 900 });
      await page.goto(url + route);
      const first = page.locator('main a img[src*="/uploads/"]').first();
      await expect(first).toBeVisible();
      await expect(first).toHaveAttribute('loading', 'eager');
      await expect(first).toHaveAttribute('fetchpriority', 'high');
      await shot(page, id, width);
      const images = await page.locator('main a img[src*="/uploads/"]').evaluateAll((imgs) => imgs.map((img) => ({ src: img.currentSrc, loading: img.loading, priority: img.fetchPriority, width: img.getBoundingClientRect().width, height: img.getBoundingClientRect().height, complete: img.complete && img.naturalWidth > 0 })));
      expect(images).toHaveLength(2);
      expect(images[0].complete).toBe(true);
      expect(images[1].loading).toBe('lazy');
      expect(images[1].priority).toBe('auto');
      expect(requests.find((r) => r.url === images[0].src)?.priority).toBe('High');
      Object.assign(results.at(-1), { images, requests: [...requests], lcp: await page.evaluate(() => window.lcpEntries) });
    }
  }
  // Show a populated week, not only today's empty week.
  const week = new Date();
  week.setUTCDate(week.getUTCDate() + 7);
  await page.goto(`${url}/calendar?view=week&date=${week.toISOString().slice(0, 10)}`);
  await expect(page.getByText('Romans 8').first()).toBeVisible();
  for (const width of [390, 768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await shot(page, 'populated-week', width);
  }
  await context.close();
} finally {
  await browser.close();
  await writeFile(`${dir}/results.json`, JSON.stringify({ url, seed, fixture, uploads, results }, null, 2) + '\n');
}
