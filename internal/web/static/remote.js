// lea web #416: an agent's terminal, read-only and live; browser notifications while the tab is
// in the background; the deck's folders. Loaded after app.js, whose hooks it fills. As in app.js,
// what agents wrote (their terminals included) is only ever set as text.
'use strict';

// --- the terminal view ----------------------------------------------------------------------

// The 16 colours of a terminal, as CSS classes (remote.css); 256 and true colours go through
// the element's style object (CSSOM, which the CSP allows), never a style attribute.
const XTERM = (() => {
  const base = ['#000000', '#cd3131', '#0dbc79', '#e5e510', '#2472c8', '#bc3fbc', '#11a8cd', '#e5e5e5',
    '#666666', '#f14c4c', '#23d18b', '#f5f543', '#3b8eea', '#d670d6', '#29b8db', '#ffffff'];
  const out = base.slice();
  const lv = [0, 95, 135, 175, 215, 255];
  for (let r = 0; r < 6; r++) for (let g = 0; g < 6; g++) for (let b = 0; b < 6; b++) out.push(`rgb(${lv[r]},${lv[g]},${lv[b]})`);
  for (let i = 0; i < 24; i++) { const v = 8 + i * 10; out.push(`rgb(${v},${v},${v})`); }
  return out;
})();

// sgr applies one SGR sequence's parameters to the pen.
function sgr(pen, params) {
  const p = params === '' ? [0] : params.split(/[;:]/).map((x) => (x === '' ? 0 : Number(x)));
  for (let i = 0; i < p.length; i++) {
    const n = p[i];
    if (n === 0) Object.assign(pen, { fg: null, bg: null, bold: false, dim: false, italic: false, under: false, inverse: false, strike: false });
    else if (n === 1) pen.bold = true;
    else if (n === 2) pen.dim = true;
    else if (n === 3) pen.italic = true;
    else if (n === 4) pen.under = true;
    else if (n === 7) pen.inverse = true;
    else if (n === 9) pen.strike = true;
    else if (n === 22) pen.bold = pen.dim = false;
    else if (n === 23) pen.italic = false;
    else if (n === 24) pen.under = false;
    else if (n === 27) pen.inverse = false;
    else if (n === 29) pen.strike = false;
    else if (n >= 30 && n <= 37) pen.fg = n - 30;
    else if (n >= 90 && n <= 97) pen.fg = n - 90 + 8;
    else if (n === 39) pen.fg = null;
    else if (n >= 40 && n <= 47) pen.bg = n - 40;
    else if (n >= 100 && n <= 107) pen.bg = n - 100 + 8;
    else if (n === 49) pen.bg = null;
    else if (n === 38 || n === 48) {
      let c = null;
      if (p[i + 1] === 5 && i + 2 < p.length) { c = p[i + 2] >= 0 && p[i + 2] < 256 ? p[i + 2] : null; i += 2; }
      else if (p[i + 1] === 2 && i + 4 < p.length) {
        const [r, g, b] = p.slice(i + 2, i + 5).map((v) => Math.max(0, Math.min(255, v | 0)));
        c = `rgb(${r},${g},${b})`; i += 4;
      }
      if (n === 38) pen.fg = c; else pen.bg = c;
    }
  }
}

function colour(c) { return typeof c === 'number' ? XTERM[c] : c; }

// span is the text drawn with the pen.
function span(text, pen) {
  const s = document.createElement('span');
  s.textContent = text;
  let fg = pen.fg, bg = pen.bg;
  if (pen.inverse) { [fg, bg] = [bg, fg]; s.classList.add('t-inv'); }
  if (typeof fg === 'number' && fg < 16) s.classList.add('t-f' + fg); else if (fg !== null) s.style.color = colour(fg);
  if (typeof bg === 'number' && bg < 16) s.classList.add('t-b' + bg); else if (bg !== null) s.style.backgroundColor = colour(bg);
  if (pen.bold) s.classList.add('t-bold');
  if (pen.dim) s.classList.add('t-dim');
  if (pen.italic) s.classList.add('t-it');
  if (pen.under) s.classList.add('t-u');
  if (pen.strike) s.classList.add('t-s');
  return s;
}

// ESCAPES matches what a captured line may hold besides text: SGR (kept), other CSI, OSC
// (hyperlinks, titles: dropped) and lone escapes or control characters (dropped).
const ESCAPES = /\x1b\[([0-9;:?<=>]*)([ -/]*[@-~])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[()*+#%][0-9A-Za-z@]?|\x1b[@-_a-z0-9=>]?|[\x00-\x08\x0b-\x1f\x7f]/g;

// renderLine turns one captured line into spans; the pen carries over from line to line, as in
// the terminal.
function renderLine(line, pen) {
  const out = [];
  let at = 0;
  for (const m of line.matchAll(ESCAPES)) {
    if (m.index > at) out.push(span(line.slice(at, m.index), pen));
    if (m[2] === 'm') sgr(pen, m[1]);
    at = m.index + m[0].length;
  }
  if (at < line.length) out.push(span(line.slice(at), pen));
  return out;
}

function renderScreen(pre, sc) {
  const pen = { fg: null, bg: null };
  const stick = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 30;
  const lines = sc.lines || [];
  pre.replaceChildren(...lines.map((l) => {
    const d = document.createElement('div');
    d.className = 't-line';
    d.append(...renderLine(l, pen));
    if (!d.childNodes.length) d.append(' ');
    return d;
  }));
  pre.dataset.size = `${sc.width || 0}x${sc.height || 0}`;
  if (stick) pre.scrollTop = pre.scrollHeight;
}

const term = { key: '', es: null, on: false };

function termStop() {
  if (term.es) term.es.close();
  term.es = null;
  term.key = '';
}

function termStart(key) {
  if (term.key === key && term.es) return;
  termStop();
  term.key = key;
  const pre = $('d-term');
  const note = $('d-term-note');
  pre.replaceChildren();
  note.textContent = 'connecting…';
  const es = new EventSource('/api/terminal?key=' + encodeURIComponent(key));
  term.es = es;
  es.addEventListener('screen', (ev) => {
    note.textContent = 'live, read-only';
    renderScreen(pre, JSON.parse(ev.data));
  });
  es.addEventListener('terminal-error', (ev) => { note.textContent = JSON.parse(ev.data).error; });
  es.onerror = () => { if (es.readyState === EventSource.CLOSED) note.textContent = 'the terminal stream ended'; };
}

function showTab(which) {
  term.on = which === 'terminal';
  $('d-tab-thread').setAttribute('aria-selected', String(!term.on));
  $('d-tab-term').setAttribute('aria-selected', String(term.on));
  $('thread').hidden = term.on;
  $('d-termbox').hidden = !term.on;
  if (term.on && ui.open) termStart(ui.open); else termStop();
}

// The detail's tabs: its messages, or its terminal.
(function addTerminal() {
  const tabs = el('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Agent view' },
    el('button', { type: 'button', id: 'd-tab-thread', role: 'tab', 'aria-selected': 'true', onclick: () => showTab('thread') }, 'Messages'),
    el('button', { type: 'button', id: 'd-tab-term', role: 'tab', 'aria-selected': 'false', title: 'Its terminal, live and read-only (v)', onclick: () => showTab('terminal') }, 'Terminal'));
  const box = el('div', { id: 'd-termbox', class: 'termbox', hidden: true },
    el('p', { id: 'd-term-note', class: 'sub', 'aria-live': 'polite' }),
    el('pre', { id: 'd-term', class: 'term', tabindex: '0', 'aria-label': 'The agent\'s terminal, read-only' }));
  $('thread').before(tabs, box);
  hooks.detail.push((a) => {
    if (term.on && term.key !== a.key) termStart(a.key);
  });
  hooks.close.push(() => { termStop(); });
  hooks.keys.v = () => { if (!ui.open && ui.selected) openDetail(ui.selected); if (ui.open) showTab(term.on ? 'thread' : 'terminal'); };
})();

// --- notifications --------------------------------------------------------------------------

const notes = { seen: null, asked: false };

function notifyOn() {
  try { return 'Notification' in window && Notification.permission === 'granted' && localStorage.getItem('lea-web-notify') !== 'off'; } catch { return false; }
}

function bell() {
  const b = $('btn-notify');
  if (!('Notification' in window)) { b.hidden = true; return; }
  const on = notifyOn();
  b.setAttribute('aria-pressed', String(on));
  b.title = on ? 'Notifications on while this tab is in the background (click: off)' : 'Notify me while this tab is in the background';
  b.classList.toggle('on', on);
}

// pending lists what needs the user: permissions (pN), spawn requests (sN), grant requests (gN),
// the tower's questions; each with a stable id and a line of text.
function pending(st) {
  const name = (k) => { const r = row(k); return r ? display(r) : String(k || '').split('/').pop(); };
  const out = [];
  for (const p of st.approvals || []) out.push({ id: 'p' + p.id, text: `p${p.id} · ${name(p.agent)} asks to use ${p.tool}` });
  for (const r of st.requests || []) out.push({ id: 's' + r.id, text: `s${r.id} · ${name(r.agent || r.from)} asks for an agent` });
  for (const g of st.grant_requests || []) out.push({ id: 'g' + g.id, text: `g${g.id} · ${name(g.agent || g.from)} asks for a grant${g.action ? ': ' + g.action : ''}` });
  for (const q of st.questions || []) if (!q.state || q.state === 'open') out.push({ id: 'q' + q.id, text: `${name(q.agent)} asks you: ${q.body || ''}` });
  return out;
}

function hidden() { return document.hidden || document.visibilityState === 'hidden' || !document.hasFocus(); }

hooks.state.push((st) => {
  const now = pending(st);
  document.title = now.length ? `(${now.length}) lea web` : 'lea web';
  const ids = new Set(now.map((p) => p.id));
  if (notes.seen === null) { notes.seen = ids; return; }
  if (notifyOn() && hidden()) {
    for (const p of now) {
      if (notes.seen.has(p.id)) continue;
      try {
        const n = new Notification('lea: ' + st.container, { body: p.text.slice(0, 200), tag: 'lea-' + st.container + '-' + p.id });
        n.onclick = () => { window.focus(); n.close(); };
      } catch { /* not allowed here */ }
    }
  }
  notes.seen = ids;
});

(function addBell() {
  const b = el('button', { type: 'button', id: 'btn-notify', class: 'ghost icon', 'aria-label': 'Notifications', 'aria-pressed': 'false' }, '🔔');
  b.addEventListener('click', async () => {
    if (notifyOn()) {
      try { localStorage.setItem('lea-web-notify', 'off'); } catch { /* no storage */ }
    } else {
      try { localStorage.removeItem('lea-web-notify'); } catch { /* no storage */ }
      if (Notification.permission !== 'granted') {
        try { await Notification.requestPermission(); } catch { /* refused */ }
        if (Notification.permission !== 'granted') toast('The browser refuses notifications for this page: allow them in its site settings.', true);
      }
    }
    bell();
  });
  $('btn-theme').before(b);
  bell();
})();

// --- folders --------------------------------------------------------------------------------

const folders = { list: null, colours: [], es: null };

function folderOf(key) { return (folders.list || []).findIndex((f) => (f.agents || []).includes(key)); }

function saveFolders(list, what) {
  folders.list = list;
  render();
  act(what, 'folders_set', { folders: list }).catch(() => {});
}

function cloneFolders() { return (folders.list || []).map((f) => ({ ...f, agents: [...(f.agents || [])] })); }

function nextFolderID() {
  let n = 0;
  for (const f of folders.list || []) n = Math.max(n, Number(f.id.slice(1)) || 0);
  return 'f' + (n + 1);
}

// moveTo puts agent key in folder id ('' : the root; '+': a new folder, named by the user).
function moveTo(key, id) {
  if (id === '+') {
    folderDialog(null, (name) => {
      const list = cloneFolders().map((f) => ({ ...f, agents: f.agents.filter((k) => k !== key) }));
      list.push({ id: nextFolderID(), name, agents: [key] });
      saveFolders(list, 'Folder made');
    });
    return;
  }
  const list = cloneFolders().map((f) => ({ ...f, agents: f.agents.filter((k) => k !== key) }));
  const f = list.find((x) => x.id === id);
  if (f) f.agents.push(key);
  saveFolders(list, id ? 'Moved to ' + f.name : 'Moved out of its folder');
}

// folderDialog asks a folder's name and colour (f null: a new folder); done gets them.
function folderDialog(f, done) {
  const d = $('dlg-folder');
  $('folder-title').textContent = f ? 'Folder ' + f.name : 'New folder';
  $('folder-name').value = f ? f.name : '';
  const sel = $('folder-colour');
  sel.replaceChildren(el('option', { value: '', text: 'none' }), ...folders.colours.map((c) => el('option', { value: c, text: c })));
  sel.value = (f && f.color) || '';
  $('folder-delete').hidden = !f;
  d.returnValue = '';
  d.showModal();
  d.addEventListener('close', () => {
    if (d.returnValue === 'delete' && f) {
      saveFolders(cloneFolders().filter((x) => x.id !== f.id), 'Folder deleted: its agents are at the root');
      return;
    }
    const name = $('folder-name').value.trim();
    if (d.returnValue !== 'ok' || !name) return;
    if (!f) { done(name, sel.value); return; }
    saveFolders(cloneFolders().map((x) => (x.id === f.id ? { ...x, name, color: sel.value || undefined } : x)), 'Folder saved');
  }, { once: true });
}

// The rows in folder order: each folder's agents in the user's order (none while it is folded),
// then the others in tree order, as the deck lists them.
hooks.order = (rows) => {
  if (!folders.list || !folders.list.length) return rows;
  const by = new Map(rows.map((r) => [r.key, r]));
  const used = new Set();
  const out = [];
  for (const f of folders.list) {
    for (const k of f.agents || []) {
      const r = by.get(k);
      if (!r || used.has(k)) continue;
      used.add(k);
      if (!f.folded) out.push({ ...r, depth: 0, folder: f.id });
    }
  }
  for (const r of rows) if (!used.has(r.key)) out.push(r);
  return out;
};

hooks.strips = (rows) => {
  if (!folders.list || !folders.list.length) return rows.map(strip);
  const out = [];
  const keys = new Set(ui.state ? ((ui.state.snapshot && ui.state.snapshot.agents) || []).map((a) => a.key) : []);
  for (const f of folders.list) {
    const n = (f.agents || []).filter((k) => keys.has(k)).length;
    out.push(el('li', { class: 'folder' + (f.color ? ' fc-' + f.color : '') + (f.folded ? ' folded' : ''), 'data-folder': f.id },
      el('button', { type: 'button', class: 'fold', 'aria-expanded': String(!f.folded), title: f.folded ? 'Unfold' : 'Fold',
        onclick: () => saveFolders(cloneFolders().map((x) => (x.id === f.id ? { ...x, folded: !x.folded } : x)), '') }, f.folded ? '▸' : '▾'),
      el('span', { class: 'fname', text: f.name }),
      el('span', { class: 'fcount', text: String(n) }),
      el('button', { type: 'button', class: 'ghost fedit', title: 'Rename, colour or delete', 'aria-label': 'Edit folder ' + f.name, onclick: () => folderDialog(f) }, '⋯')));
    for (const r of rows) if (r.folder === f.id) out.push(strip(r));
  }
  for (const r of rows) if (!r.folder) out.push(strip(r));
  return out;
};

(function addFolders() {
  const sel = el('select', { id: 'd-folder', 'aria-label': 'Folder' });
  $('d-role').closest('label').after(el('label', { class: 'role' }, 'Folder ', sel));
  sel.addEventListener('change', () => { if (ui.open) moveTo(ui.open, sel.value); });
  hooks.detail.push((a) => {
    const fi = folderOf(a.key);
    sel.replaceChildren(el('option', { value: '', text: 'none' }),
      ...(folders.list || []).map((f) => el('option', { value: f.id, text: f.name })),
      el('option', { value: '+', text: 'New folder…' }));
    sel.value = fi >= 0 ? folders.list[fi].id : '';
    sel.disabled = folders.list === null;
  });
  document.body.append(el('dialog', { id: 'dlg-folder', class: 'dlg' },
    el('form', { method: 'dialog' },
      el('h2', { id: 'folder-title' }),
      el('label', {}, 'Name ', el('input', { id: 'folder-name', maxlength: '24', autocomplete: 'off' })),
      el('label', {}, 'Colour ', el('select', { id: 'folder-colour' })),
      el('div', { class: 'row' },
        el('button', { value: 'delete', id: 'folder-delete', class: 'danger', formnovalidate: true }, 'Delete'),
        el('button', { value: 'cancel', formnovalidate: true }, 'Cancel'),
        el('button', { value: 'ok', class: 'primary', id: 'folder-ok' }, 'Save')))));
  const connectFolders = () => {
    const es = new EventSource('/api/folders');
    folders.es = es;
    es.addEventListener('folders', (ev) => {
      const v = JSON.parse(ev.data);
      folders.list = v.folders || [];
      folders.colours = v.colours || [];
      if (ui.state) render();
    });
    es.addEventListener('folders-error', (ev) => { toast(JSON.parse(ev.data).error, true); });
    es.onerror = () => { if (es.readyState === EventSource.CLOSED) setTimeout(connectFolders, 2000); };
  };
  connectFolders();
})();
