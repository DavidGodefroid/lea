// A minimal Chrome DevTools Protocol client for the site's end-to-end test: Node's own fetch and
// WebSocket (Node 22 or later), no dependency. It starts a headless Chrome (CHROME, or the first
// of google-chrome, chromium… on the PATH), or connects to one already running (CDP=http://HOST:PORT).
import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

export function findChrome() {
  if (process.env.CHROME) return process.env.CHROME;
  for (const name of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser', 'chrome', 'headless-shell']) {
    for (const dir of (process.env.PATH || '').split(delimiter)) {
      if (dir && existsSync(join(dir, name))) return join(dir, name);
    }
  }
  return null;
}

export async function launch() {
  if (process.env.CDP) {
    const v = await (await fetch(process.env.CDP.replace(/\/$/, '') + '/json/version')).json();
    return { ws: v.webSocketDebuggerUrl, close: () => {} };
  }
  const bin = findChrome();
  if (!bin) return null;
  const profile = mkdtempSync(join(tmpdir(), 'lea-site-chrome-'));
  const proc = spawn(bin, ['--headless=new', '--remote-debugging-port=0', '--user-data-dir=' + profile,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars', 'about:blank'],
  { stdio: 'ignore' });
  const portFile = join(profile, 'DevToolsActivePort');
  for (let i = 0; i < 100 && !existsSync(portFile); i++) await sleep(100);
  const [port, path] = readFileSync(portFile, 'utf8').split('\n');
  return {
    ws: `ws://127.0.0.1:${port}${path}`,
    close: () => { proc.kill(); rmSync(profile, { recursive: true, force: true }); },
  };
}

export class Browser {
  static async connect(url) {
    const b = new Browser();
    b.sock = new WebSocket(url);
    b.next = 1;
    b.pending = new Map();
    b.listeners = [];
    b.sock.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data);
      if (m.id && b.pending.has(m.id)) {
        const { ok, fail } = b.pending.get(m.id);
        b.pending.delete(m.id);
        if (m.error) fail(new Error(m.error.message)); else ok(m.result);
      } else if (m.method) {
        for (const l of b.listeners) l(m);
      }
    });
    await new Promise((ok, fail) => { b.sock.addEventListener('open', ok); b.sock.addEventListener('error', fail); });
    return b;
  }

  // Every call fails after `timeout` ms, naming its method: a call Chrome never answers (an
  // evaluated promise that never settles) must fail the test, not hang the CI job.
  send(method, params = {}, sessionId, timeout = 30000) {
    const id = this.next++;
    this.sock.send(JSON.stringify({ id, method, params, sessionId }));
    return new Promise((ok, fail) => {
      const t = setTimeout(() => {
        this.pending.delete(id);
        const what = params.expression ? ' ' + params.expression.replace(/\s+/g, ' ').slice(0, 80) : '';
        fail(new Error(`timeout after ${timeout} ms: ${method}${what}`));
      }, timeout);
      this.pending.set(id, { ok: (v) => { clearTimeout(t); ok(v); }, fail: (e) => { clearTimeout(t); fail(e); } });
    });
  }

  on(fn) { this.listeners.push(fn); return () => { this.listeners = this.listeners.filter((l) => l !== fn); }; }

  async page() {
    const { targetId } = await this.send('Target.createTarget', { url: 'about:blank' });
    const { sessionId } = await this.send('Target.attachToTarget', { targetId, flatten: true });
    return new Page(this, targetId, sessionId);
  }

  close() { this.sock.close(); }
}

export class Page {
  constructor(browser, targetId, sessionId) { Object.assign(this, { browser, targetId, sessionId }); }

  send(method, params) { return this.browser.send(method, params, this.sessionId); }

  on(method, fn) { return this.browser.on((m) => { if (m.sessionId === this.sessionId && m.method === method) fn(m.params); }); }

  once(method, timeout = 15000) {
    return new Promise((ok, fail) => {
      const t = setTimeout(() => { off(); fail(new Error('timeout waiting for ' + method)); }, timeout);
      const off = this.on(method, (p) => { clearTimeout(t); off(); ok(p); });
    });
  }

  async eval(expression) {
    const r = await this.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true, userGesture: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
    return r.result.value;
  }

  async goto(url) {
    const loaded = this.once('Page.loadEventFired');
    await this.send('Page.navigate', { url });
    await loaded;
    await this.eval('document.fonts.ready.then(() => true)');
  }

  close() { return this.browser.send('Target.closeTarget', { targetId: this.targetId }); }
}
