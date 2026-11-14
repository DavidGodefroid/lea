// End-to-end test of lea web (#414) in headless Chrome.
//
//   go build -o build/lea ./cmd/lea && go build -o build/lea-web-fixture ./tests/web/fixture
//   node tests/web/e2e.mjs
//
// Starts the test daemon (tests/web/fixture: lea's daemon on a fresh store, one container
// lea-e2e, no tmux or docker), then `lea web` against it, opens the printed URL with its
// one-time token in headless Chrome and checks that:
//   - the token signs the browser in once, and the API refuses requests without the cookie (401)
//     or with a foreign Origin (403);
//   - an agent the daemon starts appears on the board, and its task report, within 2 s;
//   - a note sent from its thread reaches its inbox; the control tower's chat sends a request;
//   - the spawn form starts a labelled agent, N a control tower; rename, pause, hold and stop;
//   - a permission prompt waits for the browser, and Allow answers it; once the browser is gone
//     the next one is not held (the agent's own dialog asks at once);
//   - a lead's grant requests appear grouped with their one-off, durable and not-covered lines,
//     and Approve / Approve all answer them; the tower's open question is answered from the
//     page, the tower gets the answer and the next question opens (#415);
//   - the allowances screen lists the capabilities, the egress screen says allowing is durable;
//   - the page has no console error and no horizontal overflow at 375 px, light and dark.
// SCREENSHOTS=DIR keeps screenshots. Without Chrome it says so and exits 0, unless
// WEB_E2E=required (CI). LEA_BIN and LEA_WEB_FIXTURE name the binaries (build/ by default).
import { spawn } from 'node:child_process';
import { request } from 'node:http';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
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
    console.log(`::error title=web e2e::${name}: ${JSON.stringify(detail)}`.replace(/\r?\n/g, ' '));
  }
}

// raw sends one request with exactly these headers (fetch forbids Host and Cookie) and
// returns its status.
function raw(url, method, headers, body) {
  return new Promise((ok, fail) => {
    const r = request(url, { method, headers }, (res) => { res.resume(); ok(res.statusCode); });
    r.on('error', fail);
    r.end(body);
  });
}

// lines reads a child's stdout as lines, each handed to the waiters that match it.
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
  const tmp = mkdtempSync(join(tmpdir(), 'lwe-'));
  const env = { ...process.env, LEA_DAEMON_SOCK: join(tmp, 'd.sock') };
  delete env.LEA_BUS_SOCK;
  const fixture = spawn(process.env.LEA_WEB_FIXTURE || join(ROOT, 'build/lea-web-fixture'), [], { env, stdio: ['pipe', 'pipe', 'inherit'] });
  const fx = lines(fixture);
  let web;
  const browser = await Browser.connect(chrome.ws);
  const shots = process.env.SCREENSHOTS;
  if (shots) mkdirSync(shots, { recursive: true });
  try {
    await fx.wait((l) => JSON.parse(l).event === 'ready');
    // op sends the fixture one command and returns its answer.
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
    check('lea web prints a loopback URL with a token', url.startsWith('http://127.0.0.1:'), url);

    // The API without the cookie, and with a foreign Origin.
    const post = (method, headers) => raw(`${base}/api/${method}`, 'POST', { 'Content-Type': 'application/json', Origin: base, ...headers }, '{}');
    check('the API answers 401 without the cookie', (await post('providers_list')) === 401);
    check('the event stream answers 401 without the cookie', (await raw(`${base}/api/events`, 'GET', {})) === 401);

    const page = await browser.page();
    const errors = [];
    page.on('Runtime.exceptionThrown', (p) => errors.push(p.exceptionDetails.exception?.description || p.exceptionDetails.text));
    page.on('Runtime.consoleAPICalled', (p) => { if (p.type === 'error') errors.push(p.args.map((a) => a.value ?? a.description).join(' ')); });
    page.on('Log.entryAdded', (p) => { if (p.entry.level === 'error') errors.push(p.entry.text + ' ' + (p.entry.url || '')); });
    await page.send('Runtime.enable');
    await page.send('Log.enable');
    await page.send('Page.enable');
    await page.send('Network.enable');
    await page.send('Emulation.setFocusEmulationEnabled', { enabled: true });
    await page.goto(url);
    const $ = (expr) => page.eval(expr);
    // until polls a page expression until it is truthy and returns how long it took (ms), or -1.
    const until = async (expr, timeout = 5000) => {
      const t0 = Date.now();
      while (Date.now() - t0 < timeout) {
        if (await $(expr).catch(() => false)) return Date.now() - t0;
        await sleep(50);
      }
      return -1;
    };
    check('the token is exchanged and leaves the address', (await $('location.href')) === base + '/');
    check('the page goes live', (await until(`document.getElementById('link').textContent === 'live'`)) >= 0, await $(`document.getElementById('link').textContent`));
    const reuse = await raw(url, 'GET', {});
    check('the token works once', reuse === 401, reuse);
    const cookie = (await page.send('Network.getCookies', { urls: [base] })).cookies.find((c) => c.name === 'lea_web');
    check('the session cookie is HttpOnly and SameSite=Strict', cookie && cookie.httpOnly && cookie.sameSite === 'Strict', cookie);
    const jar = { Cookie: `lea_web=${cookie.value}` };
    const port = new URL(base).port;
    check('the cookie opens the API', (await post('providers_list', jar)) === 200);
    const forged = await post('providers_list', { ...jar, Origin: 'http://evil.example' });
    check('a foreign Origin is refused even with the cookie', forged === 403, forged);
    const rebound = await post('providers_list', { ...jar, Host: 'evil.example:' + port, Origin: 'http://evil.example:' + port });
    check('a foreign Host (DNS rebinding) is refused even with the cookie', rebound === 403, rebound);

    // An agent appears, and its task.
    await op({ op: 'spawn', name: 'dev-1', role: 'dev' });
    let ms = await until(`document.querySelector('.strip[data-key="${C}/dev-1"]')`);
    check('a new agent appears on the board within 2 s', ms >= 0 && ms < 2000, ms);
    await op({ op: 'task', agent: 'dev-1', status: 'running the tests', next: 'CI' });
    ms = await until(`(document.querySelector('.strip[data-key="${C}/dev-1"] .status') || {}).textContent === 'running the tests'`);
    check('a task report appears within 2 s', ms >= 0 && ms < 2000, ms);
    check('the strip shows next and ctx_pct', await $(`(() => { const s = document.querySelector('.strip[data-key="${C}/dev-1"]');
      return s.querySelector('.next').textContent === 'next: CI' && s.textContent.includes('ctx 42%'); })()`));

    // A note from its thread.
    await $(`document.querySelector('.strip[data-key="${C}/dev-1"]').click()`);
    check('its thread opens', (await until(`!document.getElementById('detail').hidden && document.getElementById('d-title').textContent === 'dev-1'`)) >= 0);
    await $(`(() => { document.getElementById('msg').value = 'hello from the browser'; document.getElementById('compose').requestSubmit(); return true; })()`);
    ms = await until(`[...document.querySelectorAll('#thread .msg.mine')].some(m => m.textContent.includes('hello from the browser'))`);
    check('the note shows in its thread', ms >= 0, ms);
    const inbox = await op({ op: 'inbox', agent: 'dev-1' });
    check('the note reaches its inbox as the user\'s', JSON.stringify(inbox).includes('hello from the browser') && JSON.stringify(inbox).includes('"from":"user"'), inbox);

    // The spawn form.
    await $(`document.getElementById('btn-new').click()`);
    await until(`document.getElementById('dlg-new').open`);
    await $(`(() => { const f = document.getElementById('form-new');
      f.role.value = 'reviewer'; f.label.value = 'web-review'; f.brief.value = 'review the web UI';
      document.getElementById('new-ok').click(); return true; })()`);
    ms = await until(`[...document.querySelectorAll('.strip .name')].some(n => n.textContent.startsWith('web-review'))`);
    check('the spawn form starts a labelled agent', ms >= 0, ms);
    // N: a control tower; t: its chat sends a request.
    await $(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'N', bubbles: true }))`);
    ms = await until(`[...document.querySelectorAll('.strip.tower .name')].some(n => n.textContent.startsWith('Control Tower'))`);
    check('N starts a control tower', ms >= 0, ms);
    const towerKey = await $(`document.querySelector('.strip.tower').dataset.key`);
    await $(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 't', bubbles: true }))`);
    await until(`document.getElementById('d-title').textContent.startsWith('Control Tower')`);
    await $(`(() => { document.getElementById('msg').value = 'ship the web UI'; document.getElementById('compose').requestSubmit(); return true; })()`);
    await until(`[...document.querySelectorAll('#thread .msg.mine')].some(m => m.textContent.includes('ship the web UI'))`);
    const tin = await op({ op: 'inbox', agent: towerKey.split('/')[1] });
    check('the tower chat sends the tower a request', JSON.stringify(tin).includes('ship the web UI') && JSON.stringify(tin).includes('"kind":"request"'), tin);

    // The tower's grant requests (#415): grouped, with the one-off, durable and not-covered lines
    // from the server; Approve answers one, Approve all the rest, as lea approve gN does.
    const tname = towerKey.split('/')[1];
    for (const [action, tag] of [['pr-create', ''], ['release-create', 'v0.15.*'], ['close-issue', '']]) {
      await op({ op: 'grants', agent: tname, action, repo: 'lea', github: 'o/lea', tag });
    }
    ms = await until(`document.querySelectorAll('.card.grants li[data-request^="g"]').length === 3`);
    check('pending grant requests appear, grouped, within 2 s', ms >= 0 && ms < 2000, ms);
    const advice = await $(`document.querySelector('.card.grants .advice').textContent`);
    check('the grant card shows one-off, durable and not-covered lines', advice.includes('One-off: lea approve g') &&
      advice.includes(`lea allow pr issues release --repo lea --tag 'v0.15.*'`) && advice.includes('lea allow off N') &&
      advice.includes('Not covered by pr issues release: push, merge, delete-branch, release-tag'), advice);
    if (shots) await shot(page, shots, 'grants-1280', 1280);
    await $(`document.querySelector('.card.grants li[data-request^="g"] button.allow').click()`);
    ms = await until(`document.querySelectorAll('.card.grants li[data-request^="g"]').length === 2`);
    check('Approve answers one grant request', ms >= 0, ms);
    let tin2 = await op({ op: 'inbox', agent: tname });
    check('the lead is told its grant request is approved', JSON.stringify(tin2).includes('approved by the user'), tin2);
    await $(`[...document.querySelectorAll('.card.grants button')].find(b => b.textContent.startsWith('Approve all')).click()`);
    ms = await until(`!document.querySelector('.card.grants')`);
    check('Approve all answers the rest', ms >= 0, ms);
    tin2 = await op({ op: 'inbox', agent: tname });
    check('the lead is told of both', (JSON.stringify(tin2).match(/approved by the user/g) || []).length === 2, tin2);

    // The tower's questions: the open one is answered from the page; the next one opens.
    await op({ op: 'ask', agent: tname, question: 'deploy to prod?' });
    await op({ op: 'ask', agent: tname, question: 'merge the docs?' });
    ms = await until(`(document.querySelector('.card.question .body') || {}).textContent === 'deploy to prod?' &&
      (document.querySelector('.card.question summary') || {}).textContent === '1 more question waits'`);
    check('the tower\'s open question appears with its queue', ms >= 0, ms);
    const qid = await $(`document.querySelector('.card.question').dataset.question`);
    await $(`(() => { const t = document.getElementById('answer-${qid}'); t.value = 'yes, deploy'; t.dispatchEvent(new Event('input'));
      document.querySelector('.card.question button.primary').click(); return true; })()`);
    ms = await until(`(document.querySelector('.card.question .body') || {}).textContent === 'merge the docs?'`);
    check('answering opens the next question', ms >= 0, ms);
    const tq = JSON.stringify(await op({ op: 'inbox', agent: tname }));
    check('the tower receives the answer as the user\'s', tq.includes('yes, deploy') && tq.includes('"from":"user"') && tq.includes('is now open'), tq);

    // Rename, pause, hold, stop dev-1.
    await $(`document.querySelector('.strip[data-key="${C}/dev-1"]').click()`);
    await until(`document.getElementById('d-title').textContent === 'dev-1'`);
    await $(`document.querySelector('[data-act=rename]').click()`);
    await until(`document.getElementById('dlg-rename').open`);
    await $(`(() => { document.getElementById('rename-input').value = 'builder';
      document.querySelector('#dlg-rename button.primary').click(); return true; })()`);
    check('rename', (await until(`document.getElementById('d-title').textContent === 'builder (dev-1)'`)) >= 0, await $(`document.getElementById('d-title').textContent`));
    await $(`document.querySelector('[data-act=pause]').click()`);
    check('pause', (await until(`document.querySelector('.strip[data-key="${C}/dev-1"]').textContent.includes('paused')`)) >= 0);
    await $(`document.querySelector('[data-act=hold]').click()`);
    check('hold its inbox', (await until(`document.querySelector('.strip[data-key="${C}/dev-1"]').textContent.includes('inbox held')`)) >= 0);

    // A permission prompt waits for the browser; Allow answers it.
    let from = fx.seen.length;
    fixture.stdin.write(JSON.stringify({ op: 'approve', agent: 'dev-1', tool: 'Bash', command: 'make test' }) + '\n');
    ms = await until(`[...document.querySelectorAll('.prompt h3')].some(h => h.textContent.includes('Bash'))`);
    check('a pending permission appears', ms >= 0 && ms < 2000, ms);
    check('the prompt offers Allow, Deny and Always', await $(`(() => { const t = document.querySelector('.prompt').textContent;
      return t.includes('Allow') && t.includes('Deny') && t.includes('Always') && t.includes('make test'); })()`));
    if (shots) await shot(page, shots, 'prompt-1280', 1280);
    await $(`document.querySelector('.prompt button.allow').click()`);
    const v = JSON.parse(await fx.wait((l) => JSON.parse(l).event === 'verdict', 10000, from));
    check('Allow answers it', v.result && v.result.verdict === 'allow' && v.result.id > 0, v);
    check('the prompt goes away', (await until(`!document.querySelector('.prompt')`)) >= 0);

    // Stop, with its confirmation.
    await $(`document.querySelector('[data-act=stop]').click()`);
    await until(`document.getElementById('dlg-confirm').open`);
    await $(`document.getElementById('confirm-ok').click()`);
    check('stop', (await until(`document.querySelector('.strip[data-key="${C}/dev-1"]').classList.contains('state-done')`)) >= 0);

    // Phone width, both themes.
    for (const scheme of ['light', 'dark']) {
      await page.send('Emulation.setDeviceMetricsOverride', { width: 375, height: 800, deviceScaleFactor: 1, mobile: true });
      await page.send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: scheme }] });
      await $(`document.getElementById('btn-back').click()`);
      await sleep(100);
      const r = await $(`(() => { const d = document.documentElement;
        return { overflow: d.scrollWidth - d.clientWidth, bg: getComputedStyle(document.body).backgroundColor }; })()`);
      check(`375 px ${scheme}: no horizontal overflow`, r.overflow <= 0, r);
      check(`375 px ${scheme}: its theme`, scheme === 'dark' ? r.bg === 'rgb(19, 26, 33)' : r.bg === 'rgb(217, 224, 230)', r.bg);
      if (shots) await shot(page, shots, `board-${scheme}-375`, 375);
    }
    check('no console error', errors.length === 0, errors);

    // The allowances screen lists the capabilities of grants.Capabilities; the egress screen says
    // there is no one-off egress approval. (After the console check: without the launcher here,
    // their host calls answer 400.)
    await page.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
    await $(`document.getElementById('btn-allow').click()`);
    ms = await until(`document.getElementById('dlg-allow').open && document.querySelectorAll('#allow-caps input[name=cap]').length >= 9`);
    const capNames = await $(`[...document.querySelectorAll('#allow-caps input')].map(i => i.value).join(' ')`);
    check('the allowances screen offers every capability and the deliver preset', ms >= 0 &&
      capNames === 'deliver push pr merge delete-branch issues stop-finished release-tag release', capNames);
    if (shots) await shot(page, shots, 'allowances-1280', 1280);
    await $(`document.getElementById('dlg-allow').close()`);
    await $(`document.getElementById('btn-egress').click()`);
    check('the egress screen says allowing a host is durable', (await until(`document.getElementById('dlg-egress').open &&
      document.getElementById('dlg-egress').textContent.includes('no one-off egress approval')`)) >= 0);
    await $(`document.getElementById('dlg-egress').close()`);

    // The browser gone, prompts no longer wait for it.
    await page.close();
    await sleep(1500);
    from = fx.seen.length;
    const t0 = Date.now();
    fixture.stdin.write(JSON.stringify({ op: 'approve', agent: 'dev-2-unknown', tool: 'Bash', command: 'ls' }) + '\n');
    const v2 = JSON.parse(await fx.wait((l) => JSON.parse(l).event === 'verdict', 10000, from));
    check('without a browser the agent\'s own dialog asks at once', v2.result && v2.result.id === 0 && Date.now() - t0 < 3000, v2);
  } finally {
    if (web) web.kill();
    fixture.kill();
    browser.close();
    await chrome.close();
    rmSync(tmp, { recursive: true, force: true });
  }
  console.log();
  if (fails.length) { console.log(`${fails.length} of ${runs} failed`); return 1; }
  console.log(`all ${runs} passed`);
  return 0;
}

async function shot(page, dir, name, width) {
  await page.send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width < 600 });
  const { data } = await page.send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(join(dir, name + '.png'), Buffer.from(data, 'base64'));
}

const fatal = (e) => {
  console.log(`FAIL ${e.stack || e}`);
  if (process.env.GITHUB_ACTIONS) console.log(`::error title=web e2e::${String(e.stack || e).replace(/\r?\n/g, ' ')}`);
};
const watchdog = setTimeout(() => { fatal(new Error('the e2e run took more than 3 minutes')); process.exit(1); }, 3 * 60 * 1000);
watchdog.unref();
try {
  process.exitCode = await main();
} catch (e) {
  fatal(e);
  process.exitCode = 1;
}
