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
  { stdio: ['ignore', 'ignore', 'pipe'] });
  // Chrome's stderr, its last 4 KiB, to say why a start failed.
  let stderr = '';
  proc.stderr.setEncoding('utf8');
  proc.stderr.on('data', (d) => { stderr = (stderr + d).slice(-4096); });
  let exit = null;
  const exited = new Promise((ok) => proc.once('exit', (code, signal) => { exit = signal || code; ok(); }));
  // A cold start on a CI runner took more than the 10 s once waited (#434): wait up to 60 s
  // (CHROME_START_TIMEOUT_MS) for Chrome to write its port, and stop at once if it exits. A
  // failed start kills Chrome, which otherwise kept the run alive until the 5-minute watchdog.
  const portFile = join(profile, 'DevToolsActivePort');
  const deadline = Date.now() + Number(process.env.CHROME_START_TIMEOUT_MS || 60000);
  let port, path;
  while (exit === null && Date.now() < deadline) {
    if (existsSync(portFile)) {
      // Chrome writes two lines, the port then the browser's path: wait for both.
      [port, path] = readFileSync(portFile, 'utf8').split('\n');
      if (port && path) break;
    }
    await sleep(100);
  }
  if (!(port && path)) {
    const why = exit !== null ? `Chrome exited (${exit})` : 'Chrome wrote no DevToolsActivePort in time';
    proc.kill('SIGKILL');
    await Promise.race([exited, sleep(5000, null, { ref: false })]);
    try { rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch {}
    throw new Error(`${why}: ${bin}\n--- Chrome stderr ---\n${stderr.trim() || '(empty)'}`);
  }
  return {
    ws: `ws://127.0.0.1:${port}${path}`,
    // Chrome still writes its profile for a moment after the kill: removing it before Chrome
    // exits failed with ENOTEMPTY, and the throw skipped the rest of the clean-up (the HTTP
    // server stayed open and the run never ended). Wait for the exit (5 s at most), then remove
    // it with retries; a profile left in the temporary directory fails nothing.
    close: async () => {
      proc.kill();
      await Promise.race([exited, sleep(5000, null, { ref: false })]);
      try { rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch {}
    },
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
