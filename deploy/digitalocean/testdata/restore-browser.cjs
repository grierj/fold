// Real private-recovery browser proof. This helper never intercepts requests,
// injects an authenticated session, or deletes through the API itself.
const { createRequire } = require('node:module');
const fs = require('node:fs');
const path = require('node:path');
process.umask(0o077);

async function runPrivateBrowser(options) {
  const { engine, origin = 'http://localhost:18080', targetId, artifacts, screenshot, cookieProof, timeout = 15000 } = options;
  const url = new URL(origin);
  if (url.protocol !== 'http:' || url.hostname !== 'localhost' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error('browser drill requires a localhost HTTP origin');
  if (!Number.isSafeInteger(targetId) || targetId <= 0) throw new Error('browser drill requires the seeded target id');
  fs.mkdirSync(artifacts, { recursive: true, mode: 0o700 });
  const events = []; let stage = 'launch', intentionalClose = false, context, page;
  const prefix = path.join(artifacts, `browser-${engine.name()}`);
  const record = (kind, detail) => { events.push({ time: new Date().toISOString(), stage, kind, detail, intentionalClose }); fs.writeFileSync(`${prefix}-events.json`, JSON.stringify(events, null, 2)); };
  const browser = await engine.launch({ headless: true });
  browser.on('disconnected', () => record('browser-disconnected'));
  try {
    context = await browser.newContext({ viewport: { width: 390, height: 900 } });
    context.setDefaultTimeout(timeout); context.setDefaultNavigationTimeout(30000);
    await context.tracing.start({ screenshots: true, snapshots: true, sources: true });
    page = await context.newPage();
    page.on('close', () => record('page-closed'));
    page.on('crash', () => record('page-crashed'));
    page.on('pageerror', error => record('page-error', error.message));
    page.on('console', message => { if (message.type() === 'error') record('console-error', message.text()); });
    page.on('requestfailed', request => record('request-failed', { method: request.method(), path: new URL(request.url()).pathname, error: request.failure()?.errorText }));
    page.on('response', response => { const p = new URL(response.url()).pathname; if (p.startsWith('/api/')) record('api-response', { method: response.request().method(), path: p, status: response.status() }); });
    stage = 'login';
    await page.goto(`${origin}/login`);
    await page.getByLabel('Email', { exact: true }).fill('admin@example.invalid');
    await page.getByLabel('Password', { exact: true }).fill('restore-drill-password');
    const [login] = await Promise.all([
      page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/auth/login' && r.request().method() === 'POST'),
      page.getByRole('button', { name: 'Log in', exact: true }).click(),
    ]);
    if (!login.ok()) throw new Error(`browser login returned ${login.status()}`);
    await page.waitForURL(u => !u.pathname.startsWith('/login'));
    const cookies = await context.cookies(origin);
    if (!cookies.some(c => c.name === 'fold-session' && c.secure && c.httpOnly)) throw new Error('no Secure HttpOnly Fold session retained on localhost');
    fs.writeFileSync(cookieProof, JSON.stringify(cookies.map(({ name, secure, httpOnly, sameSite }) => ({ name, secure, httpOnly, sameSite })), null, 2));
    // Use the browser's real cookie policy, including Secure localhost,
    // rather than a separate APIRequestContext implementation of cookies.
    const me = await page.evaluate(async milliseconds => {
      const response = await fetch('/api/v1/auth/me', { credentials: 'include', signal: AbortSignal.timeout(milliseconds) });
      return { status: response.status, body: await response.json() };
    }, timeout);
    if (me.status !== 200 || me.body.user?.role !== 'admin') throw new Error(`browser session is not an authenticated admin (${me.status})`);
    await page.screenshot({ path: `${prefix}-signed-in.png`, fullPage: true });
    stage = 'users';
    const [users] = await Promise.all([
      page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/admin/users' && r.request().method() === 'GET'),
      page.goto(`${origin}/admin?tab=users`),
    ]);
    if (!users.ok()) throw new Error(`browser users list returned ${users.status()}`);
    const target = (await users.json()).users?.find(u => u.id === targetId && u.email === 'a@example.invalid');
    if (!target) throw new Error('restored target missing from real browser users response');
    const search = page.getByRole('searchbox', { name: 'Search by name or email', exact: true });
    await search.fill('a@example.invalid');
    const row = page.getByRole('row').filter({ hasText: 'a@example.invalid' });
    await row.getByRole('button', { name: /^More for/ }).click();
    await row.getByRole('menuitem', { name: 'Delete', exact: true }).click();
    stage = 'delete-panel';
    const button = row.getByRole('button', { name: 'Delete account', exact: true });
    await button.waitFor({ state: 'visible' });
    await page.screenshot({ path: screenshot, fullPage: true });
    stage = 'delete';
    const [deleted] = await Promise.all([
      page.waitForResponse(r => new URL(r.url()).pathname === `/api/v1/admin/users/${targetId}` && r.request().method() === 'DELETE', { timeout: 50000 }),
      button.click(),
    ]);
    if (!deleted.ok()) throw new Error(`real browser delete returned ${deleted.status()}`);
    await row.waitFor({ state: 'hidden' });
    stage = 'complete'; record('proof-complete', { targetId });
  } catch (error) {
    record('failure', { error: error.stack || String(error), url: page?.url(), browserConnected: browser.isConnected(), pageClosed: page?.isClosed() });
    if (page && !page.isClosed()) {
      try { await page.screenshot({ path: `${prefix}-failure.png`, fullPage: true, timeout: 2000 }); } catch (e) { record('screenshot-failed', e.message); }
      try { fs.writeFileSync(`${prefix}-failure.html`, await page.content()); } catch (e) { record('html-failed', e.message); }
    }
    throw error;
  } finally {
    if (context) { try { await context.tracing.stop({ path: `${prefix}-trace.zip` }); } catch (e) { record('trace-failed', e.message); } }
    intentionalClose = true;
    await browser.close();
  }
}
module.exports = { runPrivateBrowser };
if (require.main === module) {
  const pw = createRequire(process.env.RESTORE_WEB_PACKAGE)('playwright');
  runPrivateBrowser({ engine: process.env.RESTORE_BROWSER === 'firefox' ? pw.firefox : pw.chromium, targetId: Number(process.env.RESTORE_TARGET_ID), artifacts: process.env.RESTORE_BROWSER_ARTIFACTS, screenshot: process.env.RESTORE_SCREENSHOT, cookieProof: process.env.RESTORE_BROWSER_PROOF }).catch(error => { console.error(error); process.exitCode = 1; });
}
