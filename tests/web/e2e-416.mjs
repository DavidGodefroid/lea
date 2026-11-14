// End-to-end test of lea web's terminal view, notifications and folders (#416) in headless Chrome.
//
//   go build -o build/lea ./cmd/lea && go build -o build/lea-web-fixture ./tests/web/fixture
//   node tests/web/e2e-416.mjs
//
// Starts the test daemon (tests/web/fixture) and `lea web` against it, signs headless Chrome in,
// and checks that:
//   - an agent's terminal shows in its Terminal tab, live (a new screen within 2 s), with its
//     colours, read-only (nothing to type into), its escapes and markup never interpreted;
//   - while the tab is in the background, a new permission (pN) and a tower question raise a
//     browser notification, once each; in the foreground, none;
//   - a folder made in the browser is in the deck's layout file, and one the deck writes there
//     shows in the browser;
//   - no console error, no horizontal overflow at 375 px.
// Without Chrome it says so and exits 0, unless WEB_E2E=required (CI). LEA_BIN and
// LEA_WEB_FIXTURE name the binaries (build/ by default).
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as sleep } from 'node:timers/promises';
import { Browser, launch } from '../site/cdp.mjs';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const C = 'lea-e2e';

const fails = [];
let runs = 0;
function check(name, ok, detail) {
  runs++;
  console.log((ok ? 'ok   ' : 'FAIL ') + name + (ok ? '' : ': ' + JSON.stringify(detail)));
  if (!ok) fails.push(name);
  if (!ok && process.env.GITHUB_ACTIONS && fails.length <= 10) {
    console.log(`::error title=web e2e 416::${name}: ${JSON.stringify(detail)}`.replace(/\r?\n/g, ' '));
  }
}

function lines(proc) {
  const seen = [];
  const waiters = [];
  createInterface({ input: proc.stdout }).on('line', (l) => {
    seen.push(l);
    for (const w of [...waiters]) if (w.match(l)) { waiters.splice(waiters.indexOf(w), 1); w.ok(l); }
  });
  return {
    seen,
    wait(match, timeout = 10000, from = 0) {
      const old = seen.slice(from).find(match);
      if (old) return Promise.resolve(old);
      return new Promise((ok, fail) => {
        const w = { match, ok: (l) => { clearTimeout(t); ok(l); } };
        const t = setTimeout(() => { waiters.splice(waiters.indexOf(w), 1); fail(new Error('timeout waiting for a line')); }, timeout);
        waiters.push(w);
      });
    },
  };
}

async function main() {
  const chrome = await launch();
  if (!chrome) {
    if (process.env.WEB_E2E === 'required') { console.log('FAIL no Chrome, and WEB_E2E=required'); return 1; }
    console.log('skip: no Chrome found (WEB_E2E=required makes this a failure)');
    return 0;
  }
  const tmp = mkdtempSync(join(tmpdir(), 'lwr-'));
  const xdg = join(tmp, 'xdg');
  mkdirSync(xdg);
  const env = { ...process.env, LEA_DAEMON_SOCK: join(tmp, 'd.sock'), XDG_DATA_HOME: xdg };
  delete env.LEA_BUS_SOCK;
  const layoutFile = join(xdg, 'lea', 'host', 'deck', C + '.json');
  const fixture = spawn(process.env.LEA_WEB_FIXTURE || join(ROOT, 'build/lea-web-fixture'), [], { env, stdio: ['pipe', 'pipe', 'inherit'] });
  const fx = lines(fixture);
  let web;
  const browser = await Browser.connect(chrome.ws);
  try {
    await fx.wait((l) => JSON.parse(l).event === 'ready');
    const op = async (cmd) => {
      const from = fx.seen.length;
      fixture.stdin.write(JSON.stringify(cmd) + '\n');
      const l = JSON.parse(await fx.wait((x) => { const e = JSON.parse(x); return e.event === 'done' && e.op === cmd.op; }, 10000, from));
      if (l.error) throw new Error(`${cmd.op}: ${l.error}`);
      return l.result;
    };
    web = spawn(process.env.LEA_BIN || join(ROOT, 'build/lea'), ['web', '--container', C], { env, stdio: ['ignore', 'pipe', 'inherit'] });
    const wl = lines(web);
    const url = (await wl.wait((l) => /http:\/\/127\.0\.0\.1:\d+\/\?token=[0-9a-f]{64}/.test(l))).match(/http:\S+/)[0];
    const base = new URL(url).origin;

    const page = await browser.page();
    const errors = [];
    page.on('Runtime.exceptionThrown', (p) => errors.push(p.exceptionDetails.exception?.description || p.exceptionDetails.text));
    page.on('Runtime.consoleAPICalled', (p) => { if (p.type === 'error') errors.push(p.args.map((a) => a.value ?? a.description).join(' ')); });
    page.on('Log.entryAdded', (p) => { if (p.entry.level === 'error') errors.push(p.entry.text + ' ' + (p.entry.url || '')); });
    await page.send('Runtime.enable');
    await page.send('Log.enable');
    await page.send('Page.enable');
    // The page sees a Notification it may use, and records what it shows; whether the tab is in
    // the background is the test's to say (window.__hidden).
    await page.send('Page.addScriptToEvaluateOnNewDocument', { source: `
      window.__notes = [];
      window.__hidden = false;
      class FakeNotification {
        constructor(title, o) { window.__notes.push({ title, body: o && o.body, tag: o && o.tag }); }
        close() {}
        static get permission() { return 'granted'; }
        static requestPermission() { return Promise.resolve('granted'); }
      }
      window.Notification = FakeNotification;
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => window.__hidden });
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (window.__hidden ? 'hidden' : 'visible') });
      document.hasFocus = () => !window.__hidden;
    ` });
    await page.goto(url);
    const $ = (expr) => page.eval(expr);
    const until = async (expr, timeout = 5000) => {
      const t0 = Date.now();
      while (Date.now() - t0 < timeout) {
        if (await $(expr).catch(() => false)) return Date.now() - t0;
        await sleep(50);
      }
      return -1;
    };
    check('the page goes live', (await until(`document.getElementById('link').textContent === 'live'`)) >= 0);

    // The terminal.
    await op({ op: 'spawn', name: 'dev-1', role: 'dev' });
    await until(`document.querySelector('.strip[data-key="${C}/dev-1"]')`);
    await op({ op: 'screen', agent: 'dev-1', lines: [
      '\u001b[1m$ make go-test\u001b[0m',
      '\u001b[32mok\u001b[0m  \u001b[38;5;208minternal/web\u001b[0m 0.4s',
      '\u001b]8;;https://example.com\u0007a link\u001b]8;;\u0007 <img src=x onerror="window.__xss=1"> \u001b[2Jdone',
    ] });
    await $(`document.querySelector('.strip[data-key="${C}/dev-1"]').click()`);
    await until(`!document.getElementById('detail').hidden`);
    await $(`document.getElementById('d-tab-term').click()`);
    let ms = await until(`(document.getElementById('d-term').textContent || '').includes('internal/web 0.4s')`);
    check('the Terminal tab shows the agent\'s terminal', ms >= 0, await $(`document.getElementById('d-term').textContent`));
    check('its colours are drawn', await $(`(() => { const s = [...document.querySelectorAll('#d-term .t-f2')].find((e) => e.textContent === 'ok');
      const o = [...document.querySelectorAll('#d-term span')].find((e) => e.textContent === 'internal/web');
      return !!s && !!o && o.style.color === 'rgb(255, 135, 0)' && !!document.querySelector('#d-term .t-bold'); })()`));
    check('escapes and markup are never interpreted', await $(`(() => { const t = document.getElementById('d-term');
      return !t.querySelector('img, a') && t.textContent.includes('<img src=x') && t.textContent.includes('a link') &&
        !t.textContent.includes(']8;;') && !t.textContent.includes('\\u001b') && !window.__xss; })()`));
    check('the terminal is read-only', await $(`!document.querySelector('#d-termbox input, #d-termbox textarea, #d-termbox [contenteditable]')`));
    check('it says it is live and read-only', (await until(`document.getElementById('d-term-note').textContent === 'live, read-only'`)) >= 0);
    await op({ op: 'screen', agent: 'dev-1', lines: ['$ git push', 'Everything up-to-date'] });
    ms = await until(`document.getElementById('d-term').textContent.includes('Everything up-to-date')`);
    check('a new screen shows within 2 s', ms >= 0 && ms < 2000, ms);
    await op({ op: 'spawn', name: 'dev-2', role: 'dev' });
    await until(`document.querySelector('.strip[data-key="${C}/dev-2"]')`);
    await $(`document.querySelector('.strip[data-key="${C}/dev-2"]').click()`);
    ms = await until(`document.getElementById('d-term-note').textContent.includes('has no terminal')`);
    check('an agent without a terminal says so', ms >= 0, await $(`document.getElementById('d-term-note').textContent`));
    await $(`document.getElementById('d-tab-thread').click()`);
    check('Messages hides the terminal', await $(`document.getElementById('d-termbox').hidden && !document.getElementById('thread').hidden`));

    // Notifications: none in the foreground; in the background a new permission and a question.
    let from = fx.seen.length;
    fixture.stdin.write(JSON.stringify({ op: 'approve', agent: 'dev-1', tool: 'Bash', command: 'make test' }) + '\n');
    await until(`document.querySelector('.prompt')`);
    await sleep(300);
    check('no notification while the tab is in front', (await $(`window.__notes.length`)) === 0, await $(`window.__notes`));
    check('the title counts what waits for you', (await $(`document.title`)) === '(1) lea web', await $(`document.title`));
    await $(`document.querySelector('.prompt button.allow').click()`);
    await fx.wait((l) => JSON.parse(l).event === 'verdict', 10000, from);
    await $(`window.__hidden = true`);
    from = fx.seen.length;
    fixture.stdin.write(JSON.stringify({ op: 'approve', agent: 'dev-2', tool: 'WebFetch', command: 'x' }) + '\n');
    ms = await until(`window.__notes.some((n) => /p\\d+ · dev-2 asks to use WebFetch/.test(n.body))`);
    check('a permission in the background raises a notification', ms >= 0 && ms < 2000, await $(`window.__notes`));
    await op({ op: 'spawn', name: 'lead-1', role: 'lead' });
    await op({ op: 'question', agent: 'lead-1', question: 'Merge the web PR now?' });
    ms = await until(`window.__notes.some((n) => n.body.includes('lead-1 asks you: Merge the web PR now?'))`);
    check('a tower question in the background raises a notification', ms >= 0, await $(`window.__notes`));
    await sleep(500);
    const counts = await $(`window.__notes.reduce((m, n) => (m[n.tag] = (m[n.tag] || 0) + 1, m), {})`);
    check('each one notifies once', Object.values(counts).every((n) => n === 1) && Object.keys(counts).length === 2, counts);
    await $(`window.__hidden = false`);
    await $(`document.querySelector('.prompt button.allow').click()`);
    await fx.wait((l) => JSON.parse(l).event === 'verdict', 10000, from);

    // Folders: the browser makes one, the deck's file has it; the deck writes one, the browser shows it.
    await $(`document.querySelector('.strip[data-key="${C}/dev-1"]').click()`);
    await until(`document.getElementById('d-title').textContent === 'dev-1'`);
    await $(`(() => { const s = document.getElementById('d-folder'); s.value = '+'; s.dispatchEvent(new Event('change')); return true; })()`);
    await until(`document.getElementById('dlg-folder').open`);
    await $(`(() => { document.getElementById('folder-name').value = 'web ui'; document.getElementById('folder-ok').click(); return true; })()`);
    ms = await until(`[...document.querySelectorAll('.folder .fname')].some((f) => f.textContent === 'web ui')`);
    check('a folder made in the browser shows on the board', ms >= 0);
    let layout = null;
    for (let i = 0; i < 40 && !layout; i++) {
      try { const l = JSON.parse(readFileSync(layoutFile, 'utf8')); if (l.folders && l.folders.length) layout = l; } catch { /* not yet */ }
      if (!layout) await sleep(50);
    }
    check('it is in the deck\'s layout file', layout && layout.folders[0].name === 'web ui' && layout.folders[0].agents[0] === `${C}/dev-1`, layout);
    check('the agent sits under its folder', await $(`(() => { const f = document.querySelector('.folder');
      return f && f.nextElementSibling && f.nextElementSibling.dataset.key === '${C}/dev-1'; })()`));
    layout.folders.push({ id: 'f9', name: 'from the deck', color: 'teal', agents: [`${C}/dev-2`] });
    layout.folders[0].folded = true;
    writeFileSync(layoutFile, JSON.stringify(layout));
    ms = await until(`[...document.querySelectorAll('.folder.fc-teal .fname')].some((f) => f.textContent === 'from the deck')`);
    check('a folder the deck writes shows in the browser', ms >= 0 && ms < 3000, ms);
    check('a folder folded in the deck is folded here', await $(`document.querySelector('.folder[data-folder="f1"]').classList.contains('folded') &&
      !document.querySelector('.strip[data-key="${C}/dev-1"]')`));

    // Phone width.
    await page.send('Emulation.setDeviceMetricsOverride', { width: 375, height: 800, deviceScaleFactor: 1, mobile: true });
    await $(`document.querySelector('.strip[data-key="${C}/dev-2"]').click()`);
    await $(`document.getElementById('d-tab-term').click()`);
    await sleep(200);
    const r = await $(`(() => { const d = document.documentElement; return d.scrollWidth - d.clientWidth; })()`);
    check('375 px: no horizontal overflow', r <= 0, r);
    if (process.env.SCREENSHOTS) {
      mkdirSync(process.env.SCREENSHOTS, { recursive: true });
      const { data } = await page.send('Page.captureScreenshot', { format: 'png' });
      writeFileSync(join(process.env.SCREENSHOTS, 'terminal-375.png'), Buffer.from(data, 'base64'));
    }
    check('no console error', errors.length === 0, errors);
  } finally {
    if (web) web.kill();
    fixture.kill();
    browser.close();
    await chrome.close();
    if (existsSync(tmp)) rmSync(tmp, { recursive: true, force: true });
  }
  console.log();
  if (fails.length) { console.log(`${fails.length} of ${runs} failed`); return 1; }
  console.log(`all ${runs} passed`);
  return 0;
}

const fatal = (e) => {
  console.log(`FAIL ${e.stack || e}`);
  if (process.env.GITHUB_ACTIONS) console.log(`::error title=web e2e 416::${String(e.stack || e).replace(/\r?\n/g, ' ')}`);
};
const watchdog = setTimeout(() => { fatal(new Error('the e2e run took more than 3 minutes')); process.exit(1); }, 3 * 60 * 1000);
watchdog.unref();
try {
  process.exitCode = await main();
} catch (e) {
  fatal(e);
  process.exitCode = 1;
}
