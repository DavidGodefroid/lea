// End-to-end test of leadeck.dev (#363) in headless Chrome.
//
//   node tests/site/e2e.mjs
//
// Assembles the site (tools/site/assemble.sh), serves it on 127.0.0.1 on an unusual port (PORT,
// 47363 by default: other sites may be served on the common ones), then loads every page in the
// light and the dark scheme, 1440 and 390 px wide, and checks:
//   - no console error, no uncaught exception, no failed request;
//   - no horizontal overflow;
//   - the page background is the theme's --paper, the headings use the family's display face;
//   - on pages with an install box, the Copy button copies its commands to the clipboard.
// SCREENSHOTS=DIR saves a full-page screenshot of each. Without Chrome it says so and exits 0,
// unless SITE_E2E=required (CI). CHROME names the binary; CDP=http://HOST:PORT uses a running one.
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Browser, launch } from './cdp.mjs';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const PAGES = ['/', '/install/', '/how-it-works/', '/security/', '/faq/', '/404.html'];
const WIDTHS = [1440, 390];
const SCHEMES = ['light', 'dark'];
const TYPES = { '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.js': 'text/javascript', '.svg': 'image/svg+xml',
  '.png': 'image/png', '.woff2': 'font/woff2', '.txt': 'text/plain', '.xml': 'application/xml', '.sh': 'text/plain' };

function paper() {
  // --paper of each theme, from the kit's tokens.css.
  const css = readFileSync(join(ROOT, 'site/assets/tokens.css'), 'utf8');
  const dark = css.indexOf('@media (prefers-color-scheme: dark)');
  const pick = (s) => s.match(/--paper:\s*(#[0-9a-f]{6})/)[1];
  const rgb = (h) => `rgb(${parseInt(h.slice(1, 3), 16)}, ${parseInt(h.slice(3, 5), 16)}, ${parseInt(h.slice(5, 7), 16)})`;
  return { light: rgb(pick(css.slice(0, dark))), dark: rgb(pick(css.slice(dark))) };
}

function serve(dir) {
  return createServer((req, res) => {
    let path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
    if (path.endsWith('/')) path += 'index.html';
    const file = normalize(join(dir, path));
    let body;
    try {
      if (!file.startsWith(dir)) throw new Error('outside');
      body = readFileSync(file);
    } catch {
      res.writeHead(404, { 'content-type': TYPES['.html'] });
      res.end(readFileSync(join(dir, '404.html')));
      return;
    }
    res.writeHead(200, { 'content-type': TYPES[extname(file)] || 'application/octet-stream' });
    res.end(body);
  });
}

async function main() {
  const chrome = await launch();
  if (!chrome) {
    if (process.env.SITE_E2E === 'required') { console.log('FAIL no Chrome, and SITE_E2E=required'); return 1; }
    console.log('skip: no Chrome found (SITE_E2E=required makes this a failure)');
    return 0;
  }
  const tmp = mkdtempSync(join(tmpdir(), 'lea-site-'));
  const out = join(tmp, '_site');
  execFileSync('sh', [join(ROOT, 'tools/site/assemble.sh'), out]);
  const port = Number(process.env.PORT || 47363);
  const server = serve(out);
  await new Promise((ok, fail) => { server.once('error', fail); server.listen(port, '127.0.0.1', ok); });
  const base = `http://127.0.0.1:${port}`;
  const shots = process.env.SCREENSHOTS;
  if (shots) mkdirSync(shots, { recursive: true });
  const bg = paper();
  const fails = [];
  let runs = 0;
  const check = (name, ok, detail) => {
    runs++;
    console.log((ok ? 'ok   ' : 'FAIL ') + name + (ok ? '' : ': ' + JSON.stringify(detail)));
    if (!ok) fails.push(name);
    // In GitHub Actions a failure is also an annotation, readable without the job's log.
    if (!ok && process.env.GITHUB_ACTIONS && fails.length <= 10) {
      console.log(`::error title=site e2e::${name}: ${JSON.stringify(detail)}`.replace(/\r?\n/g, ' '));
    }
  };

  const browser = await Browser.connect(chrome.ws);
  try {
    await browser.send('Browser.grantPermissions', { origin: base, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'] });
    for (const scheme of SCHEMES) {
      for (const width of WIDTHS) {
        for (const path of PAGES) {
          const tag = `${path} ${scheme} ${width}px`;
          const page = await browser.page();
          const errors = [];
          page.on('Runtime.exceptionThrown', (p) => errors.push(p.exceptionDetails.exception?.description || p.exceptionDetails.text));
          page.on('Runtime.consoleAPICalled', (p) => { if (p.type === 'error') errors.push(p.args.map((a) => a.value ?? a.description).join(' ')); });
          page.on('Log.entryAdded', (p) => { if (p.entry.level === 'error') errors.push(p.entry.text + ' ' + (p.entry.url || '')); });
          page.on('Network.loadingFailed', (p) => { if (!p.canceled) errors.push('failed: ' + p.errorText); });
          page.on('Network.responseReceived', (p) => { if (p.response.status >= 400 && !p.response.url.endsWith('/404.html')) errors.push(p.response.status + ' ' + p.response.url); });
          await page.send('Runtime.enable');
          await page.send('Log.enable');
          await page.send('Network.enable');
          await page.send('Page.enable');
          await page.send('Emulation.setFocusEmulationEnabled', { enabled: true });
          await page.send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width < 600 });
          await page.send('Emulation.setEmulatedMedia', { features: [
            { name: 'prefers-color-scheme', value: scheme }, { name: 'prefers-reduced-motion', value: 'reduce' }] });
          await page.goto(base + path);

          const r = await page.eval(`(() => {
            const d = document.documentElement;
            return {
              overflow: d.scrollWidth - d.clientWidth,
              wide: [...document.querySelectorAll('main *')].filter(e => e.getBoundingClientRect().right > d.clientWidth + 1
                && !e.closest('pre, .table-wrap, .demo-frame')).slice(0, 3).map(e => e.tagName + '.' + e.className),
              bg: getComputedStyle(document.body).backgroundColor,
              h1: getComputedStyle(document.querySelector('h1')).fontFamily,
              boxes: document.querySelectorAll('.install').length,
              buttons: document.querySelectorAll('.install button.copy').length,
            };
          })()`);
          check(tag + ': no horizontal overflow', r.overflow <= 0 && r.wide.length === 0, r);
          check(tag + ': --paper background', r.bg === bg[scheme], r.bg);
          check(tag + ': display face on h1', r.h1.startsWith('"Zilla Slab"'), r.h1);

          if (r.boxes) {
            check(tag + ': a Copy button per install box', r.buttons === r.boxes, r);
            await page.eval('navigator.clipboard.writeText("")');
            const box = await page.eval(`(() => {
              const b = document.querySelector('.install button.copy');
              b.scrollIntoView({ block: 'center' });
              const r = b.getBoundingClientRect();
              return { x: r.x + r.width / 2, y: r.y + r.height / 2,
                want: [...b.closest('.install').querySelectorAll('.cmd')].map(c => c.textContent.trim()).join('\\n') };
            })()`);
            for (const type of ['mousePressed', 'mouseReleased']) {
              await page.send('Input.dispatchMouseEvent', { type, x: box.x, y: box.y, button: 'left', clickCount: 1 });
            }
            await page.eval('new Promise(ok => setTimeout(ok, 200))');
            const got = await page.eval(`navigator.clipboard.readText().then(t => ({ text: t,
              button: document.querySelector('.install button.copy').textContent,
              status: document.querySelector('.install [role=status]').textContent }))`);
            check(tag + ': Copy copies the install commands', got.text === box.want && got.button === 'Copied' && got.status === 'Copied',
              { want: box.want, got });
          }
          check(tag + ': no console error', errors.length === 0, errors);

          if (shots) {
            const { cssContentSize } = await page.send('Page.getLayoutMetrics');
            await page.send('Emulation.setDeviceMetricsOverride', { width, height: Math.ceil(cssContentSize.height), deviceScaleFactor: 1, mobile: width < 600 });
            const { data } = await page.send('Page.captureScreenshot', { format: 'png' });
            const name = (path === '/' ? 'home' : path.replace(/^\/|\/$|\.html$/g, '')) + `-${scheme}-${width}.png`;
            writeFileSync(join(shots, name), Buffer.from(data, 'base64'));
          }
          await page.close();
        }
      }
    }
  } finally {
    browser.close();
    chrome.close();
    server.close();
    rmSync(tmp, { recursive: true, force: true });
  }
  console.log();
  if (fails.length) { console.log(`${fails.length} of ${runs} failed`); return 1; }
  console.log(`all ${runs} passed`);
  return 0;
}

const fatal = (e) => {
  console.log(`FAIL ${e.stack || e}`);
  if (process.env.GITHUB_ACTIONS) console.log(`::error title=site e2e::${String(e.stack || e).replace(/\r?\n/g, ' ')}`);
};

// The whole run takes well under a minute; past 5 something hangs that no call timeout caught.
const watchdog = setTimeout(() => { fatal(new Error('the e2e run took more than 5 minutes')); process.exit(1); }, 5 * 60 * 1000);
watchdog.unref();

try {
  process.exitCode = await main();
} catch (e) {
  fatal(e);
  process.exitCode = 1;
}
