// Synthetic busy-church preconditions. Uses normal authenticated APIs only.
// Run after drive-ui and SEED_ONLY=1 evaluate-directory. Never schedules mail.
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { chromium } = require('playwright');
const url = process.env.VERIFY_URL, evidence = process.env.VERIFY_EVIDENCE;
if (!url || new URL(url).hostname !== '127.0.0.1' || !evidence || !process.env.VERIFY_DATABASE_URL?.includes('fold_verify_')) throw new Error('Use verify-fold env');
const { seed } = JSON.parse(await readFile(`${evidence}/ui/results.json`, 'utf8'));
const dir = `${evidence}/busy-church`; await mkdir(dir, { recursive: true });
const browser = await chromium.launch(); const manifest = { groups: [], users: [], events: [], messages: [], rooms: [], registrations: [] };
const first = ['Alex', 'Blair', 'Casey', 'Drew', 'Elliot', 'Francis', 'Harper', 'Jamie', 'Morgan', 'Taylor'];
const last = ['Baker', 'Chen', 'Diaz', 'Evans', 'Foster', 'Garcia', 'Hughes', 'Ibrahim', 'Jackson', 'Kim'];
const day = (n) => new Date(Date.now() + n * 86400000).toISOString().slice(0, 10);
try {
  const page = await browser.newPage(); await page.goto(url + '/login');
  await page.getByLabel('Email', { exact: true }).fill('verify-admin@example.com'); await page.getByLabel('Password', { exact: true }).fill('password1234');
  await page.getByRole('button', { name: 'Log in', exact: true }).click(); await page.waitForURL((u) => u.pathname !== '/login');
  const { csrfToken } = await (await page.request.get(url + '/api/v1/auth/csrf')).json();
  async function api(method, path, data, retries = 0) {
    const response = await page.request.fetch(url + '/api/v1' + path, { method, data, headers: { 'X-CSRF-Token': csrfToken } });
    const body = await response.json();
    if (!response.ok()) {
      if (body.code === 'date_add_throttled' && retries < 2) {
        console.log('Respecting the 30/minute date-add limit; retrying after 61 seconds.');
        await new Promise((resolve) => setTimeout(resolve, 61000));
        return api(method, path, data, retries + 1);
      }
      throw new Error(`${method} ${path}: ${response.status()} ${JSON.stringify(body)}`);
    }
    return body;
  }
  async function groupDates(id) {
    const dates = []; let cursor = '';
    do {
      const result = await api('GET', `/groups/${id}/events?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
      dates.push(...result.events); cursor = result.nextCursor;
    } while (cursor);
    return dates;
  }
  const groups = (await api('GET', '/groups')).groups;
  if (groups.length !== 50) throw new Error('Seed exactly fifty groups first.');
  const users = (await api('GET', '/admin/users')).users;
  if (users.some((u) => !/^verify-(admin|leader|member)@example.com$|^busy-\d{3}@example.com$/.test(u.email))) throw new Error('Unexpected accounts in fixture database.');
  for (let i = 0; i < 97; i++) {
    const email = `busy-${String(i + 1).padStart(3, '0')}@example.com`;
    if (users.some((u) => u.email === email)) continue;
    const { user } = await api('POST', '/admin/users', { ageBand: 'adult', email, name: `${first[i % 10]} ${last[Math.floor(i / 10)]}`, displayName: first[i % 10], password: 'password1234', role: 'member' });
    users.push(user);
    if (i % 20 === 0) console.log(`Created ${users.length} users`);
  }
  if (users.length !== 100) throw new Error('Expected 100 users');
  manifest.users = users.map(({ id, name, email, status }) => ({ id, name, email, status }));
  const sam = users.find((u) => u.email === 'verify-member@example.com');
  const extras = users.filter((u) => u.email.startsWith('busy-')).sort((a, b) => a.email.localeCompare(b.email));
  const main = groups.find((g) => g.id === seed.groupId);
  const selected = [main, groups.find((g) => g.name === 'School Supply Volunteers'), groups.find((g) => g.name === 'Lunch Break Bible Study')];
  if (selected.some((g) => !g)) throw new Error('Missing expected fixture groups.');
  for (const group of selected) await api('PUT', `/groups/${group.id}`, { memberLimit: null });
  for (const user of extras.slice(0, 57)) await api('POST', `/admin/groups/${main.id}/members`, { userId: user.id, role: 'member' });
  for (const user of extras.slice(0, 39)) await api('POST', `/admin/groups/${selected[1].id}/members`, { userId: user.id, role: 'member' });
  const mine = [main, ...groups.filter((g) => g.id !== main.id).sort((a, b) => a.id - b.id).slice(0, 24)];
  for (const group of mine.slice(1)) await api('POST', `/admin/groups/${group.id}/members`, { userId: sam.id, role: 'member' });
  console.log('Seeded large rosters and 25 memberships');
  for (const group of selected) {
    const existing = await groupDates(group.id);
    for (let i = 0; i < 30; i++) {
      const title = `Busy gathering ${String(i + 1).padStart(2, '0')}: ${group.name}`;
      let event = existing.find((e) => e.title === title);
      const date = day(1 + i % 10);
      if (!event) event = (await api('POST', `/groups/${group.id}/events`, {
        title, description: 'Synthetic busy-church experiment, not a real gathering.', startsLocal: `${date}T${String(8 + i % 12).padStart(2, '0')}:00`,
        durationMinutes: 60, tzid: 'America/Boise', location: 'Fellowship Hall', visibility: 'public', recurrenceRule: '',
      })).event;
      manifest.events.push({ id: event.eventId ?? event.id, groupId: group.id, title, date });
    }
  }
  const longTitle = 'Daily prayer and community care';
  const allMain = await groupDates(main.id);
  let series = allMain.find((e) => e.title === longTitle);
  if (!series) series = (await api('POST', `/groups/${main.id}/events`, {
    title: longTitle, description: 'Synthetic 40-date series.', startsLocal: `${day(1)}T07:00`, durationMinutes: 45,
    tzid: 'America/Boise', location: 'Chapel', visibility: 'public', recurrenceRule: 'FREQ=DAILY;COUNT=40',
  })).event;
  manifest.seriesId = series.eventId ?? series.id;
  const churchTitles = ['Community Lunch', 'Newcomers Welcome', 'Volunteer Orientation', 'Food Pantry Collection', 'Family Game Night', 'Prayer Evening', 'Community Choir Concert', 'Neighborhood Cleanup', 'School Supply Drive', 'Christmas Planning', 'Community Breakfast', 'Care Team Training', 'All Church Picnic', 'Parent Workshop', 'Mission Partners Update', 'Baptism Celebration', 'Community Garden Day', 'Youth Fundraiser', 'Safety Training', 'Hospitality Workshop', 'Thanksgiving Meal', 'Craft Fair', 'Leadership Gathering', 'Family Service Day', 'Evening of Worship'];
  const church = (await api('GET', '/events')).events;
  for (let i = 0; i < churchTitles.length; i++) {
    const title = churchTitles[i]; let event = church.find((e) => e.title === title);
    if (!event) event = (await api('POST', '/events', {
      title, description: 'A synthetic church-wide gathering for the content-volume experiment. Details would explain who it is for and what to bring.',
      startsLocal: `${day(1 + i % 7)}T${String(9 + i % 11).padStart(2, '0')}:00`, durationMinutes: 90, tzid: 'America/Boise', location: 'Main Campus, Fellowship Hall', visibility: 'public', recurrenceRule: '',
    })).event;
    manifest.events.push({ id: event.eventId ?? event.id, title, churchWide: true });
  }
  // Large attendee list on one date; separate notices for Sam across 30 dates.
  const target = manifest.events.find((e) => e.groupId === main.id);
  for (const user of extras.slice(0, 48)) {
    await api('POST', `/events/${target.id}/registrations/add`, { userId: user.id, date: target.date });
    manifest.registrations.push({ eventId: target.id, userId: user.id });
  }
  for (const event of manifest.events.filter((e) => e.groupId === main.id)) await api('POST', `/events/${event.id}/registrations/add`, { userId: sam.id, date: event.date });
  manifest.attendeeEvent = target;
  const attendeeDate = (await api('GET', `/events/${target.id}`)).occurrences.find((o) => o.date === target.date);
  manifest.attendeeCount = attendeeDate.attendees.length;
  console.log('Seeded 90 group events, 40-date series, 25 church events and attendee lists');
  const drafts = (await api('GET', '/messages')).messages;
  for (let i = 1; i <= 40; i++) {
    const subject = `Busy church draft ${String(i).padStart(2, '0')}`;
    let message = drafts.find((m) => m.subject === subject);
    if (!message) message = (await api('POST', '/messages', { subject, bodyHtml: '<p>Synthetic draft for the experiment. Not scheduled or sent.</p>', audience: { type: 'group', groupId: main.id }, pushAlert: false })).message;
    manifest.messages.push({ id: message.id, subject });
  }
  await api('PUT', '/admin/settings/rooms', { enabled: true });
  const rooms = (await api('GET', '/admin/rooms')).rooms;
  for (let i = 1; i <= 18; i++) {
    const name = `Room ${String(i).padStart(2, '0')}`; const building = ['North Campus', 'South Campus', 'Community Center'][i % 3];
    let room = rooms.find((r) => r.name === name && r.building === building);
    if (!room) room = (await api('POST', '/admin/rooms', { name, building, capacity: i % 3 ? 25 : 80 })).room;
    manifest.rooms.push({ id: room.id, name, building });
  }
  for (const group of groups) {
    const detail = await api('GET', `/groups/${group.id}`);
    manifest.groups.push({ id: group.id, name: group.name, members: detail.group.memberCount, loadedDates: detail.upcomingEvents.length, hasMore: detail.hasMore });
  }
  manifest.memberGroupCount = mine.length;
  manifest.churchEventCount = (await api('GET', '/events')).events.length;
  console.log(`Busy seed ready: 100 users, 50 groups, ${manifest.churchEventCount} church dates, 40 drafts, 18 rooms`);
} finally { await browser.close(); await writeFile(`${dir}/manifest.json`, JSON.stringify(manifest, null, 2) + '\n'); }
