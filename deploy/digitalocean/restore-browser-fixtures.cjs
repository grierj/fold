// Separate, disposable browser regression. Uses the actual built SPA with
// synthetic API fixtures, not a substitute for the real Docker/Postgres drill.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const dist = process.env.RESTORE_BROWSER_DIST || path.join(root, 'web/dist');
const { chromium } = require(path.join(root, 'web/node_modules/playwright'));
const { runPrivateBrowser } = require('./testdata/restore-browser.cjs');
process.umask(0o077);
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'fold-browser-fixtures-'));
const admin = { id: 1, name: 'Drill Admin', displayName: '', email: 'admin@example.invalid', role: 'admin', status: 'active', timezone: 'UTC', accountType: 'adult', phone: '', avatarUrl: '' };
const target = { ...admin, id: 2, name: 'Member A', email: 'a@example.invalid', role: 'member' };
async function scenario(mode) {
  const artifacts = path.join(work, mode); fs.mkdirSync(artifacts);
  let people = [admin, target], deletes = 0, browser;
  const server = http.createServer(async (req, res) => {
    const p = new URL(req.url, 'http://localhost').pathname;
    const send = (data, code = 200) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(data)); };
    const signedIn = (req.headers.cookie || '').includes('fold-session=fixture');
    if (p.startsWith('/api/')) {
      if (p === '/api/v1/auth/csrf') return send({ csrfToken: 'fixture' });
      if (p === '/api/v1/auth/login') { res.setHeader('Set-Cookie', 'fold-session=fixture; Secure; HttpOnly; SameSite=Lax; Path=/'); return send({ user: admin, canCompose: true }); }
      if (p === '/api/v1/auth/me') return send({ user: signedIn ? admin : null, canCompose: signedIn });
      if (!signedIn) return send({ error: 'signed out' }, 401);
      if (p === '/api/v1/admin/users') {
        if (mode === 'list-error') return send({ error: 'fixture list failure' }, 500);
        if (mode === 'closed-browser') { await browser.close(); res.destroy(); return; }
        return send({ users: people });
      }
      if (p === '/api/v1/admin/users/2' && req.method === 'DELETE') { deletes++; people = [admin]; return send({ ok: true }); }
      if (p.endsWith('/sole-leader-groups')) return send({ groups: [] });
      if (p.endsWith('/sole-host-events')) return send({ events: [], candidates: [] });
      if (p === '/api/v1/admin/jobs') return send({ jobs: [], more: false });
      if (p.includes('groups') || p.includes('pending') || p.includes('deletions')) return send({ groups: [] });
      if (p === '/api/v1/site') return send({ siteName: 'Drill', timezone: 'UTC', roomsEnabled: false, pushEnabled: false });
      if (p.includes('notifications')) return send({ notifications: [], unread: 0 });
      return send({});
    }
    const file = path.join(dist, p.startsWith('/assets/') ? p : 'index.html');
    if (!fs.existsSync(file)) { res.writeHead(404); res.end(); return; }
    res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.woff2') ? 'font/woff2' : 'text/html');
    res.end(fs.readFileSync(file));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `http://localhost:${server.address().port}`;
  // The browser is real. Only this regression's server supplies synthetic API
  // data, and every request outside that server is refused by the fixture.
  const engine = { name: () => 'chromium', launch: async options => {
    browser = await chromium.launch(options);
    const create = browser.newContext.bind(browser);
    browser.newContext = async options => {
      const context = await create(options);
      await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
      return context;
    };
    return browser;
  } };
  try {
    let error;
    try { await runPrivateBrowser({ engine, origin, targetId: 2, artifacts, screenshot: path.join(artifacts, 'delete-390.png'), cookieProof: path.join(artifacts, 'cookies.json'), timeout: 5000 }); } catch (e) { error = e; }
    const events = JSON.parse(fs.readFileSync(path.join(artifacts, 'browser-chromium-events.json')));
    if (mode === 'success') {
      assert.equal(error, undefined); assert.equal(deletes, 1);
      assert(events.some(e => e.kind === 'proof-complete'));
      assert(JSON.parse(fs.readFileSync(path.join(artifacts, 'cookies.json'))).some(c => c.name === 'fold-session' && c.secure && c.httpOnly));
      assert(fs.statSync(path.join(artifacts, 'delete-390.png')).size > 0);
    } else {
      assert(error); assert.equal(deletes, 0);
      assert(events.some(e => e.kind === 'failure' && !e.intentionalClose && e.stage === 'users'));
      if (mode === 'list-error') {
        assert.match(error.message, /users list returned 500/);
        assert(fs.statSync(path.join(artifacts, 'browser-chromium-failure.html')).size > 0);
      } else assert(events.some(e => e.kind === 'browser-disconnected' && !e.intentionalClose));
    }
    console.log(`restore browser fixture ${mode}: ok`);
  } finally {
    await browser?.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
}
(async () => {
  assert(fs.existsSync(path.join(dist, 'index.html')), 'build the SPA before running browser fixtures');
  for (const mode of ['success', 'list-error', 'closed-browser']) await scenario(mode);
})().then(() => fs.rmSync(work, { recursive: true }), error => { console.error(error); console.error(`browser fixture artifacts retained: ${work}`); process.exitCode = 1; });
