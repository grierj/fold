// Historical pre-implementation 50-group evaluator. On the current layout use
// SEED_ONLY=1, then verify-directory.mjs. Fixture writes require an isolated
// verify-fold instance and use the app's normal authenticated API.
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { measure } from '../web/e2e/ui-measure.mjs';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium } = require('playwright');
const { expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const url = process.env.VERIFY_URL;
const evidence = process.env.VERIFY_EVIDENCE;
if (!url || new URL(url).hostname !== '127.0.0.1' || !evidence || !process.env.VERIFY_DATABASE_URL?.includes('fold_verify_')) throw new Error('Use verify-fold env for an isolated run.');
await readFile(`${evidence}/ui/results.json`); // Require the standard seed first.
const dir = `${evidence}/directory-50`;
await mkdir(dir, { recursive: true });
const sets = [
  ['bible-study', ['adults', 'online', 'young-adults', 'prayer'], [
    'Early Bird Bible Study', 'Lunch Break Bible Study', 'Discovering the Gospel of John', 'Questions about Faith',
    'Monday Evening Scripture and Supper', 'Reading the Psalms Together', 'Young Adults Exploring the Bible', 'Sunday Morning Bible Foundations',
  ]],
  ['families', ['parents', 'kids', 'adults', 'online'], [
    'Young Families at the Park', 'Parents of Preschoolers', 'Foster and Adoptive Families', 'Parents of Children with Additional Support Needs',
    'Saturday Family Breakfast', 'Grandparents Raising Grandchildren', 'Single Parents Supporting One Another', 'Home Educators Meet Up',
  ]],
  ['care', ['adults', 'prayer', 'seniors', 'online'], [
    'Grief and Hope', 'Caregivers Coffee Circle', 'Walking Together through Chronic Illness', 'Prayer for Our Neighbors',
    'New in Town', 'Life after Retirement', 'Hospital Visiting Team', 'Recovery and Encouragement',
  ]],
  ['outreach', ['service', 'adults', 'students', 'families'], [
    'Community Food Pantry', 'Neighborhood Meal Team', 'Welcome and Hospitality', 'English Conversation Partners',
    'School Supply Volunteers', 'Winter Coat Collection', 'Community Garden Helpers', 'Local Shelter Serving Team',
  ]],
  ['outdoors', ['sports', 'adults', 'young-adults', 'families'], [
    'Lakeside Walking Group', 'Saturday Hiking', 'Cycling and Conversation', 'Beginner Pickleball',
    'Evening Trail Walks', 'Family Camping', 'Community Softball', 'Accessible Outdoor Adventures',
  ]],
  ['music', ['arts', 'adults', 'students', 'worship'], [
    'Community Choir', 'Acoustic Worship Circle', 'Crafting for Others', 'Photography and Fellowship',
    'Book Club', 'Creative Writing and Reflection', 'Drama and Storytelling', 'Handbells for Beginners',
  ]],
];
const fixtures = sets.flatMap(([tag, audiences, names]) => names.map((name, i) => ({ name, tags: [tag, audiences[i % audiences.length]] })));
if (fixtures.length !== 48) throw new Error('Expected 48 additions to the two-group standard seed.');
const locations = ['Fellowship Hall', 'North Campus, Room 201', 'Community Center, upstairs meeting room', 'Library', 'Westside Park picnic shelter', 'South Campus, accessible ground-floor lounge'];
const browser = await chromium.launch();
const results = [];
const tasks = [];
let inventory = [];
async function newPage() {
  const ctx = await browser.newContext({ locale: 'en-US', timezoneId: 'America/Boise', viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  return page;
}
async function signIn(page, who) {
  await page.goto(url + '/login');
  await page.getByLabel('Email', { exact: true }).fill(`verify-${who}@example.com`);
  await page.getByLabel('Password', { exact: true }).fill('password1234');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.waitForURL((u) => u.pathname !== '/login');
}
async function api(page, method, path, data) {
  const { csrfToken } = await (await page.request.get(url + '/api/v1/auth/csrf')).json();
  const r = await page.request.fetch(url + '/api/v1' + path, { method, data, headers: { 'X-CSRF-Token': csrfToken } });
  if (!r.ok()) throw new Error(`${method} ${path}: ${r.status()} ${await r.text()}`);
  return r.json();
}
const cards = (page) => page.locator('#groups a.fold-list-row');
async function capture(page, id, width, full = false) {
  await page.waitForLoadState('networkidle');
  if (await page.locator('#coming-up-heading').count()) {
    await expect(page.locator('section[aria-labelledby="coming-up-heading"] li')).toHaveCount(4);
  }
  const metrics = await page.evaluate(() => {
    const section = document.getElementById('groups');
    const list = [...section.querySelectorAll('a.fold-list-row')];
    const rects = list.map((el) => el.getBoundingClientRect());
    const chips = [...section.querySelectorAll('.chip')];
    const chipBox = chips[0]?.parentElement.getBoundingClientRect();
    const search = section.querySelector('input');
    const top = (el) => Math.round(el.getBoundingClientRect().top + scrollY);
    const first = section.parentElement.firstElementChild;
    const main = document.querySelector('main').getBoundingClientRect();
    const heights = rects.map((r) => r.height);
    const tabs = document.querySelector('nav[aria-label="Tabs"]')?.getBoundingClientRect();
    const bottomBar = tabs?.height ? Math.max(0, innerHeight - tabs.top) : 0;
    const requests = performance.getEntriesByType('resource').filter((e) => new URL(e.name).pathname === '/api/v1/groups');
    return {
      groups: list.length, pageHeight: document.documentElement.scrollHeight, viewportHeight: innerHeight,
      firstGroupY: rects.length ? Math.round(rects[0].top + scrollY) : null,
      firstGroupViewportY: rects.length ? Math.round(rects[0].top) : null,
      groupHeadingY: top(section.querySelector('h2')), searchY: top(search), searchVisible: search.getBoundingClientRect().bottom > 64 && search.getBoundingClientRect().top < innerHeight - bottomBar,
      chipCount: chips.length, chipRows: new Set(chips.map((c) => Math.round(c.getBoundingClientRect().top))).size, chipHeight: chipBox ? Math.round(chipBox.height) : 0,
      visibleGroups: rects.filter((r) => r.top < innerHeight - bottomBar && r.bottom > 64).length,
      fullVisibleGroups: rects.filter((r) => r.top >= 64 && r.bottom <= innerHeight - bottomBar).length,
      directoryWidth: Math.round(section.getBoundingClientRect().width), mainWidth: Math.round(main.width), contentWidth: Math.round(section.parentElement.getBoundingClientRect().width),
      belowAgenda: first !== section && innerWidth >= 1024 ? Math.round(section.getBoundingClientRect().bottom - first.getBoundingClientRect().bottom) : null,
      cardHeight: heights.length ? { min: Math.min(...heights), mean: heights.reduce((a, b) => a + b, 0) / heights.length, max: Math.max(...heights) } : null,
      multiLineTitles: list.filter((el) => { const h = el.querySelector('h3'); return h.getBoundingClientRect().height > parseFloat(getComputedStyle(h).lineHeight) * 1.1; }).length,
      eagerImages: section.querySelectorAll('img[loading=eager]').length, lazyImages: section.querySelectorAll('img[loading=lazy]').length,
      decodedImagesAtCapture: list.filter((el) => { const img = el.querySelector('img'); return img.complete && img.naturalWidth > 0; }).length,
      nodeCount: document.querySelectorAll('*').length,
      groupRequests: requests.map((r) => ({ duration: Math.round(r.duration * 10) / 10, decodedBytes: r.decodedBodySize })),
    };
  });
  await page.screenshot({ path: `${dir}/${id}-${width}.png`, animations: 'disabled' });
  if (full) await page.screenshot({ path: `${dir}/${id}-${width}-full.png`, fullPage: true, animations: 'disabled' });
  const m = await page.evaluate(measure);
  const { violations } = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  const axe = violations.filter((v) => ['moderate', 'serious', 'critical'].includes(v.impact)).map((v) => v.id);
  const fails = [m.overflow > 0 && `overflow ${m.overflow}`, m.cutOff.length && m.cutOff, m.clipped.length && m.clipped, m.under24.length && m.under24, axe.length && axe].filter(Boolean);
  results.push({ id, width, metrics, fails });
  console.log(`${fails.length ? 'FAIL' : 'ok'} ${id}@${width}: ${JSON.stringify(metrics)}`);
}
try {
  const admin = await newPage();
  await signIn(admin, 'admin');
  const { groups: existing } = await api(admin, 'GET', '/groups');
  const allowed = new Set(['Tuesday Bible Study', 'Youth Choir', ...fixtures.map((g) => g.name)]);
  if (existing.some((g) => !allowed.has(g.name))) throw new Error('Unexpected data: use a fresh isolated instance.');
  for (const [i, item] of fixtures.entries()) {
    let group = existing.find((g) => g.name === item.name);
    if (!group) group = (await api(admin, 'POST', '/groups', {
      ...item, description: 'Synthetic group for the 50-group directory experiment, not a real ministry.',
      location: item.tags.includes('online') ? 'Online' : locations[i % locations.length],
      imageUrl: '', visibility: 'public', memberLimit: i % 4 === 0 ? 12 : null,
    })).group;
    if (i % 3 !== 0) {
      const { events } = await api(admin, 'GET', `/groups/${group.id}/events`);
      if (!events.length) {
        const day = new Date(Date.now() + (1 + i % 28) * 86400000).toISOString().slice(0, 10);
        await api(admin, 'POST', `/groups/${group.id}/events`, {
          title: item.name, description: 'Synthetic experiment date.', startsLocal: `${day}T${i % 2 ? '18:30' : '10:00'}`,
          durationMinutes: 90, tzid: 'America/Boise', location: item.tags.includes('online') ? 'Online' : locations[i % locations.length], recurrenceRule: '',
        });
      }
    }
  }
  inventory = (await api(admin, 'GET', '/groups')).groups;
  expect(inventory).toHaveLength(50);
  await writeFile(`${dir}/inventory.json`, JSON.stringify(inventory, null, 2) + '\n');
  await admin.context().close();
  if (!process.env.SEED_ONLY) {
  for (const role of ['visitor', 'member']) {
    const page = await newPage();
    if (role === 'member') await signIn(page, 'member');
    for (const width of [390, 768, 1280]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(url);
      await expect(cards(page)).toHaveCount(50);
      await capture(page, `${role}-top`, width, true);
      await page.getByRole('heading', { name: 'Groups', exact: true }).scrollIntoViewIfNeeded();
      await page.locator('#groups').evaluate((el) => el.scrollIntoView({ block: 'start' }));
      await capture(page, `${role}-directory`, width);
      await cards(page).nth(24).evaluate((el) => el.scrollIntoView({ block: 'start' }));
      await capture(page, `${role}-middle`, width);
    }
    await page.context().close();
  }
  const page = await newPage();
  await signIn(page, 'member');
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(url);
    await expect(cards(page)).toHaveCount(50);
    const search = page.getByRole('searchbox', { name: 'Search groups', exact: true });
    const query = 'Lakeside';
    await search.fill(query);
    await expect(cards(page)).toHaveCount(1);
    await capture(page, 'search-one', width);
    await page.getByRole('heading', { name: 'Lakeside Walking Group', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Lakeside Walking Group', level: 1 })).toBeVisible();
    await page.goBack();
    await search.waitFor();
    await page.waitForLoadState('networkidle');
    await expect(cards(page).first()).toBeVisible();
    await capture(page, 'after-detail-back', width);
    tasks.push({ task: 'search-detail-back', width, queryBefore: query, queryAfter: await search.inputValue(), groupsAfter: await cards(page).count(), urlAfter: page.url() });
    await search.fill('zzzz-no-such-group');
    await expect(page.getByText('No groups match “zzzz-no-such-group”', { exact: true })).toBeVisible();
    await expect(cards(page)).toHaveCount(0);
    await capture(page, 'search-empty', width);
    await search.fill('');
    await expect(cards(page)).toHaveCount(50);
    await page.getByRole('button', { name: 'bible-study', exact: true }).click();
    const studyCount = inventory.filter((g) => g.tags.includes('bible-study')).length;
    await expect(cards(page)).toHaveCount(studyCount);
    await capture(page, 'tag-one', width);
    await page.getByRole('button', { name: 'outdoors', exact: true }).click();
    const unionCount = inventory.filter((g) => g.tags.includes('bible-study') || g.tags.includes('outdoors')).length;
    await expect(cards(page)).toHaveCount(unionCount);
    await capture(page, 'tag-two', width);
    await page.getByRole('button', { name: 'Clear', exact: true }).click();
    await expect(cards(page)).toHaveCount(50);
    tasks.push({ task: 'tag-filter-and-clear', width, studyCount, unionCount, clearedCount: await cards(page).count() });
    await page.goto(url + '/?tag=outdoors#groups');
    await expect(cards(page)).toHaveCount(inventory.filter((g) => g.tags.includes('outdoors')).length);
    await capture(page, 'shared-tag-link', width);
  }
  await page.context().close();
  }
} finally {
  await browser.close();
  await writeFile(`${dir}/results.json`, JSON.stringify({ url, groups: inventory.length, uniqueTags: [...new Set(inventory.flatMap((g) => g.tags))].sort(), scheduledGroups: inventory.filter((g) => g.nextEventAt).length, results, tasks }, null, 2) + '\n');
  await writeFile(`${dir}/result.txt`, [
    'Feature: directory-50', `Entry points: ${url}/ and /?tag=outdoors#groups; signed-out visitor and signed-in member`,
    `Groups: ${inventory.length}; screen/state checks: ${results.length}; hard layout/a11y failures: ${results.filter((r) => r.fails.length).length}`,
    ...tasks.map((t) => JSON.stringify(t)),
    'Synthetic fixtures only. No shared database or production UI changes. See results.json and inventory.json.',
  ].join('\n') + '\n');
}
if (results.some((r) => r.fails.length)) process.exitCode = 1;
