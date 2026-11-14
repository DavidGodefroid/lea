// lea web: the deck in a browser. Every fact comes from the server's event stream (the daemon's
// agents_watch); every action is one POST /api/<method>. Agent text is only ever set as text.
'use strict';

const $ = (id) => document.getElementById(id);
const TOWER_LABEL = 'Control Tower';

const ui = {
  state: null,          // the last State event
  rows: [],             // the agents, tree order
  selected: '',         // the strip the keyboard is on
  open: '',             // the strip whose thread is shown
  threadFor: '',        // the key and version the thread was last read at
  threadAt: -1,
  providers: null,
};

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k === 'text') e.textContent = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids) if (c !== null && c !== undefined && c !== false) e.append(c);
  return e;
}

let toastTimer;
function toast(text, bad) {
  const t = $('toast');
  t.textContent = text;
  t.classList.toggle('bad', !!bad);
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, bad ? 6000 : 2500);
}

async function api(method, params) {
  const r = await fetch('/api/' + method, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(params || {}),
    credentials: 'same-origin',
  });
  let body = {};
  try { body = await r.json(); } catch { /* not JSON */ }
  if (r.status === 401) signedOut();
  if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
  return body.result;
}

async function act(what, method, params) {
  try {
    const res = await api(method, params);
    if (what) toast(what);
    return res;
  } catch (e) {
    toast(e.message, true);
    throw e;
  }
}

function signedOut() {
  const l = $('link');
  l.textContent = 'not signed in: open the URL lea web printed (it works once), or start lea web again';
  l.className = 'link down';
}

// --- the model ------------------------------------------------------------------------------

function display(a) { return a.label && a.label !== a.name ? `${a.label} (${a.name})` : a.name; }

function taskOf(a) {
  const ts = (ui.state && ui.state.tasks) || [];
  return ts.find((t) => t.agent === a.name && !t.ended) || null;
}

// shownState is the deck's (issue #72): green only when nothing is pending.
function shownState(a) {
  const live = a.state === 'working' || a.state === 'idle';
  if (live && a.blocked_on) return 'blocked';
  if (a.state === 'idle' && a.limit_until) return 'limited';
  if (a.state === 'idle' && a.subagents > 0) return 'working';
  if (a.state === 'idle' && a.waiting) return 'waiting';
  return a.state || 'unknown';
}

function isTower(a) { return a.role === 'lead' && (a.label || '').startsWith(TOWER_LABEL); }

function tower() {
  const live = ui.rows.filter((a) => a.state !== 'done' && a.state !== 'stopped');
  return live.find(isTower) || live.find((a) => a.role === 'lead' && !a.parent) || null;
}

// tree: each lead, then its children, in launch order; a child whose parent is gone is a root.
function tree(agents) {
  const keys = new Set(agents.map((a) => a.key));
  const by = (a, b) => (a.created || 0) - (b.created || 0);
  const roots = agents.filter((a) => !a.parent || !keys.has(a.parent)).sort(by);
  const out = [];
  for (const r of roots) {
    out.push({ ...r, depth: 0 });
    for (const c of agents.filter((a) => a.parent === r.key).sort(by)) out.push({ ...c, depth: 1 });
  }
  return out;
}

function row(key) { return ui.rows.find((a) => a.key === key) || null; }

// --- rendering ------------------------------------------------------------------------------

function render() {
  const st = ui.state;
  $('lea-name').textContent = st.container;
  const agents = ((st.snapshot && st.snapshot.agents) || []).filter((a) => a.kind !== 'shell');
  ui.rows = tree(agents);
  if (!row(ui.selected)) ui.selected = ui.rows.length ? ui.rows[0].key : '';
  renderStrips();
  renderPrompts();
  if (typeof renderDecisions === 'function') renderDecisions();
  if (ui.open && !row(ui.open)) closeDetail();
  if (ui.open) renderDetail();
}

function renderStrips() {
  const list = $('strips');
  list.replaceChildren(...ui.rows.map(strip));
  $('empty').hidden = ui.rows.length > 0;
}

function strip(a) {
  const s = shownState(a);
  const t = taskOf(a);
  const meta = [];
  if (a.role) meta.push(el('span', { text: a.role }));
  if (a.branch) meta.push(el('span', { text: a.branch }));
  if (a.ctx_pct >= 0) meta.push(el('span', { text: `ctx ${a.ctx_pct}%` }));
  if (a.cost_usd >= 0) meta.push(el('span', { text: `$${a.cost_usd.toFixed(2)}` }));
  if (a.unread > 0) meta.push(el('span', { class: 'badge', title: 'messages waiting for it', text: `${a.unread} unread` }));
  if (a.for_user > 0) meta.push(el('span', { class: 'badge', title: 'messages for you', text: `${a.for_user} for you` }));
  if (a.paused) meta.push(el('span', { class: 'flag', text: 'paused' }));
  if (a.held) meta.push(el('span', { class: 'flag', text: a.held_count ? `inbox held (${a.held_count})` : 'inbox held' }));
  if (a.questions_open) meta.push(el('span', { class: 'flag', text: 'asks you' }));
  const li = el('li', {
    class: `strip state-${s}${a.depth ? ' child' : ''}${s === 'done' || s === 'stopped' ? ' done' : ''}${isTower(a) ? ' tower' : ''}`,
    role: 'option', 'aria-selected': a.key === ui.selected ? 'true' : 'false', tabindex: '-1', 'data-key': a.key,
    onclick: () => { ui.selected = a.key; openDetail(a.key); },
  },
  el('span', { class: 'name' }, a.label || a.name, a.label && a.label !== a.name ? el('small', { text: a.name }) : null),
  el('span', { class: 'state', text: s }),
  t && t.status ? el('span', { class: 'status', text: t.status }) : null,
  t && t.next ? el('span', { class: 'next', text: 'next: ' + t.next }) : null,
  t && t.blocked_on ? el('span', { class: 'blocker', text: 'blocked on: ' + t.blocked_on }) : null,
  el('span', { class: 'meta' }, ...meta));
  return li;
}

function inputText(raw) {
  try {
    const v = JSON.parse(raw);
    if (v && typeof v === 'object') return v.command || v.file_path || v.url || JSON.stringify(v, null, 1);
  } catch { /* cut or not JSON */ }
  return raw || '';
}

function renderPrompts() {
  const box = $('prompts');
  const ps = (ui.state.approvals || []).map((p) => {
    const a = ui.rows.find((r) => r.key === p.agent);
    const who = a ? display(a) : p.agent.split('/').pop();
    const decide = (verdict) => act(verdict === 'deny' ? 'Denied' : 'Allowed', 'approval_decide', { id: p.id, verdict }).catch(() => {});
    return el('article', { class: 'prompt', 'data-id': String(p.id) },
      el('h3', { text: `p${p.id} · ${who} asks to use ${p.tool}` }),
      el('div', { class: 'row' },
        el('button', { type: 'button', class: 'allow', onclick: () => decide('allow') }, 'Allow'),
        el('button', { type: 'button', onclick: () => decide('deny') }, 'Deny'),
        p.always ? el('button', { type: 'button', onclick: () => decide('always'), title: 'Allow it now and from now on' }, 'Always') : null),
      el('pre', { class: 'what', text: inputText(p.input) }));
  });
  box.replaceChildren(...ps);
}

function openDetail(key) {
  ui.open = key;
  ui.threadFor = '';
  $('detail').hidden = false;
  renderStrips();
  renderDetail();
}

function closeDetail() {
  ui.open = '';
  $('detail').hidden = true;
}

function renderDetail() {
  const a = row(ui.open);
  if (!a) return;
  const s = shownState(a);
  const t = taskOf(a);
  const towerMode = tower() && tower().key === a.key;
  $('d-title').textContent = display(a);
  $('d-sub').textContent = [a.role, s, a.repo, a.branch, a.profile].filter(Boolean).join(', ');
  $('d-task').replaceChildren(
    t && t.status ? el('span', { text: t.status }) : el('span', { class: 'sub', text: 'No task reported yet.' }),
    t && t.next ? el('span', { text: 'Next: ' + t.next }) : null,
    t && t.blocked_on ? el('span', { class: 'blocker', text: 'Blocked on: ' + t.blocked_on }) : null);
  const qs = (ui.state.questions || []).filter((q) => q.agent === a.key);
  const qbox = $('d-questions');
  qbox.hidden = qs.length === 0;
  qbox.replaceChildren(el('h3', { text: qs.length === 1 ? 'It asks you' : `It asks you ${qs.length} questions` }),
    el('ol', {}, ...qs.map((q) => el('li', { class: q.state === 'open' ? 'open' : '', text: q.body + (q.state === 'open' ? '' : ' (queued)') }))),
    el('p', { class: 'sub', text: qs.some((q) => q.state === 'open') ? 'Answer the open one in the panel above the board.' : 'Answer in a message below.' }));
  const ended = a.state === 'done' || a.state === 'stopped';
  for (const b of $('d-tools').querySelectorAll('button')) b.disabled = ended && b.dataset.act !== 'rename';
  $('d-tools').querySelector('[data-act=pause]').textContent = a.paused ? 'Resume' : 'Pause';
  $('d-tools').querySelector('[data-act=hold]').textContent = a.held ? 'Release inbox' : 'Hold inbox';
  $('d-role').value = a.role;
  $('d-role').disabled = ended;
  $('msg').placeholder = towerMode ? 'Ask the control tower' : 'Write to ' + (a.label || a.name);
  $('compose').dataset.kind = towerMode ? 'request' : 'note';
  if (ui.threadFor !== a.key || ui.threadAt !== ui.state.version) loadThread(a.key, ui.state.version);
}

async function loadThread(key, version) {
  ui.threadFor = key;
  ui.threadAt = version;
  let ms;
  try { ms = await api('thread_list', { key }); } catch (e) { toast(e.message, true); return; }
  if (ui.open !== key) return;
  const list = $('thread');
  const stick = list.scrollTop + list.clientHeight >= list.scrollHeight - 30;
  const a = row(key);
  const name = (k) => {
    if (k === 'user') return 'you';
    const r = row(k);
    return r ? display(r) : k.split('/').pop();
  };
  list.replaceChildren(...(ms && ms.length ? ms.map((m) => el('li', { class: 'msg ' + (m.from === 'user' ? 'mine' : 'theirs') },
    el('span', { class: 'h', text: `${name(m.from)} → ${name(m.to)} · ${m.kind} · ${new Date(m.at * 1000).toLocaleTimeString()}` }),
    m.body)) : [el('li', { class: 'none', text: `No message with ${a ? a.label || a.name : 'it'} yet.` })]));
  if (stick) list.scrollTop = list.scrollHeight;
}

// --- actions --------------------------------------------------------------------------------

function current() { return row(ui.open) || row(ui.selected); }

async function rename(a) {
  const d = $('dlg-rename');
  $('rename-input').value = a.label || '';
  d.returnValue = '';
  d.showModal();
  d.addEventListener('close', () => {
    if (d.returnValue === 'ok') act('Renamed', 'agent_label', { key: a.key, label: $('rename-input').value.trim() }).catch(() => {});
  }, { once: true });
}

function stop(a) {
  const d = $('dlg-confirm');
  $('confirm-title').textContent = 'Stop ' + display(a) + '?';
  $('confirm-text').textContent = 'Its process ends; its messages and task stay on the board.';
  d.returnValue = '';
  d.showModal();
  d.addEventListener('close', () => {
    if (d.returnValue === 'ok') act('Stopped ' + (a.label || a.name), 'agent_stop', { key: a.key }).catch(() => {});
  }, { once: true });
}

function tool(name) {
  const a = current();
  if (!a) return;
  if (name === 'rename') rename(a);
  if (name === 'pause') act(a.paused ? 'Resumed' : 'Paused', 'agent_pause', { key: a.key, paused: !a.paused }).catch(() => {});
  if (name === 'hold') act(a.held ? 'Inbox released' : 'Inbox held', 'agent_hold', { key: a.key, held: !a.held }).catch(() => {});
  if (name === 'stop') stop(a);
}

async function newAgent() {
  const d = $('dlg-new');
  const f = $('form-new');
  f.reset();
  $('new-err').textContent = '';
  const sel = $('new-profile');
  if (!ui.providers) {
    try { ui.providers = (await api('providers_list')) || []; } catch (e) { ui.providers = []; }
  }
  sel.replaceChildren(el('option', { value: '', text: 'your default' }),
    ...ui.providers.map((p) => el('option', { value: p.name, text: p.name })));
  d.showModal();
}

$('form-new').addEventListener('submit', async (ev) => {
  if (ev.submitter && ev.submitter.value !== 'ok') return;
  ev.preventDefault();
  const f = new FormData($('form-new'));
  const p = Object.fromEntries([...f.entries()].map(([k, v]) => [k, String(v).trim()]));
  $('new-ok').disabled = true;
  try {
    const r = await api('agent_new', p);
    $('dlg-new').close();
    toast('Started ' + (r.label || r.name));
    ui.selected = r.key;
  } catch (e) {
    $('new-err').textContent = e.message;
  } finally {
    $('new-ok').disabled = false;
  }
});

async function newTower() {
  try {
    const r = await act('', 'agent_new', { tower: true });
    toast('Started ' + (r.label || r.name));
    ui.selected = r.key;
    ui.open = '';
  } catch { /* told */ }
}

function talkToTower() {
  const t = tower();
  if (!t) {
    const d = $('dlg-confirm');
    $('confirm-title').textContent = 'No control tower runs';
    $('confirm-text').textContent = 'Start one? It runs a team of agents for you: you write to it, it spawns and briefs them.';
    $('confirm-ok').textContent = 'Start a control tower';
    $('confirm-ok').className = 'primary';
    d.returnValue = '';
    d.showModal();
    d.addEventListener('close', () => {
      $('confirm-ok').textContent = 'Stop';
      $('confirm-ok').className = 'danger';
      if (d.returnValue === 'ok') newTower();
    }, { once: true });
    return;
  }
  ui.selected = t.key;
  openDetail(t.key);
  $('msg').focus();
}

$('compose').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const a = row(ui.open);
  const body = $('msg').value.trim();
  if (!a || !body) return;
  const kind = $('compose').dataset.kind || 'note';
  try {
    await act('', 'msg_send', { to: a.key, body, kind });
    $('msg').value = '';
    ui.threadAt = -1;
  } catch { /* told */ }
});
$('msg').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) { ev.preventDefault(); $('compose').requestSubmit(); }
});
$('d-tools').addEventListener('click', (ev) => {
  const b = ev.target.closest('button[data-act]');
  if (b) tool(b.dataset.act);
});
$('d-role').addEventListener('change', () => {
  const a = row(ui.open);
  if (a) act('Role changed', 'agent_role', { key: a.key, role: $('d-role').value }).catch(() => { $('d-role').value = a.role; });
});
$('btn-back').addEventListener('click', closeDetail);
$('btn-new').addEventListener('click', newAgent);
$('btn-tower').addEventListener('click', talkToTower);
$('btn-help').addEventListener('click', () => $('dlg-help').showModal());

// --- theme ----------------------------------------------------------------------------------

function theme(t) {
  if (t) document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme;
}
try { theme(localStorage.getItem('lea-web-theme')); } catch { /* no storage */ }
$('btn-theme').addEventListener('click', () => {
  const dark = document.documentElement.dataset.theme
    ? document.documentElement.dataset.theme === 'dark'
    : matchMedia('(prefers-color-scheme: dark)').matches;
  const t = dark ? 'light' : 'dark';
  theme(t);
  try { localStorage.setItem('lea-web-theme', t); } catch { /* no storage */ }
});

// --- keyboard -------------------------------------------------------------------------------

document.addEventListener('keydown', (ev) => {
  if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
  if (document.querySelector('dialog[open]')) return;
  const tag = (ev.target.tagName || '').toLowerCase();
  if (tag === 'input' || tag === 'textarea' || tag === 'select') {
    if (ev.key === 'Escape') ev.target.blur();
    return;
  }
  const i = ui.rows.findIndex((a) => a.key === ui.selected);
  const move = (d) => {
    if (!ui.rows.length) return;
    ui.selected = ui.rows[Math.max(0, Math.min(ui.rows.length - 1, i + d))].key;
    renderStrips();
    if (ui.open) openDetail(ui.selected);
    const s = document.querySelector(`.strip[aria-selected="true"]`);
    if (s) s.scrollIntoView({ block: 'nearest' });
  };
  const first = (ui.state && ui.state.approvals && ui.state.approvals[0]) || null;
  const keys = {
    j: () => move(1), ArrowDown: () => move(1), k: () => move(-1), ArrowUp: () => move(-1),
    Enter: () => ui.selected && openDetail(ui.selected),
    Escape: () => closeDetail(),
    m: () => { if (!ui.open && ui.selected) openDetail(ui.selected); $('msg').focus(); },
    e: () => tool('rename'), p: () => tool('pause'), h: () => tool('hold'), x: () => tool('stop'),
    n: newAgent, N: newTower, t: talkToTower, '?': () => $('dlg-help').showModal(),
    g: () => typeof openAllowances === 'function' && openAllowances(),
    w: () => typeof openEgress === 'function' && openEgress(),
    a: () => first && act('Allowed', 'approval_decide', { id: first.id, verdict: 'allow' }).catch(() => {}),
    d: () => first && act('Denied', 'approval_decide', { id: first.id, verdict: 'deny' }).catch(() => {}),
    A: () => first && first.always && act('Allowed', 'approval_decide', { id: first.id, verdict: 'always' }).catch(() => {}),
  };
  const f = keys[ev.key];
  if (f) { ev.preventDefault(); f(); }
});

// --- the stream -----------------------------------------------------------------------------

function connect() {
  const link = $('link');
  const es = new EventSource('/api/events');
  es.addEventListener('state', (ev) => {
    ui.state = JSON.parse(ev.data);
    link.textContent = 'live';
    link.className = 'link live';
    render();
  });
  es.addEventListener('lea-error', (ev) => {
    link.textContent = JSON.parse(ev.data).error;
    link.className = 'link down';
  });
  es.onerror = async () => {
    // A 401 ends the stream for good: say how to sign in. Anything else, EventSource retries.
    if (es.readyState === EventSource.CLOSED) {
      const r = await fetch('/api/providers_list', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }).catch(() => null);
      if (r && r.status === 401) { signedOut(); return; }
      link.textContent = 'reconnecting…';
      link.className = 'link down';
      setTimeout(connect, 1000);
    } else {
      link.textContent = 'reconnecting…';
      link.className = 'link down';
    }
  };
}
connect();
