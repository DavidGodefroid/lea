// lea web (#415): what waits for the user besides permissions — the control tower's open question,
// spawn requests (sN), grant requests (gN) with their one-off and durable answers — and the
// allowances and egress screens. It uses app.js's helpers (el, api, act, toast, ui, row, display).
// Every text about what an approval covers comes from the server (grants.Advise), never from here.
'use strict';

const dec = { answer: {}, allowPreview: null, allowCaps: null };

function who(key) {
  if (!key || key === 'user') return 'you';
  const a = row(key) || row((ui.state && ui.state.container) + '/' + key);
  return a ? display(a) : key.split('/').pop();
}

function ago(at) {
  if (!at) return '';
  const s = Math.max(0, Math.round(Date.now() / 1000 - at));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return `${Math.round(s / 3600)} h ago`;
}

function lasting(s) {
  if (!s) return '';
  if (s % 86400 === 0) return `${s / 86400} d`;
  if (s % 3600 === 0) return `${s / 3600} h`;
  return `${Math.round(s / 60)} min`;
}

// --- the panel ------------------------------------------------------------------------------

function renderDecisions() {
  const st = ui.state;
  const cards = [];
  const open = (st.questions || []).filter((q) => q.state === 'open');
  for (const q of open) cards.push(questionCard(q, (st.questions || []).filter((x) => x.agent === q.agent && x.state !== 'open')));
  for (const r of st.requests || []) cards.push(spawnCard(r));
  if ((st.grant_requests || []).length) cards.push(grantCard(st.grant_requests, st.grant_advice));
  const box = $('decisions');
  // Keep what the user is typing: a card with focus or a draft is rebuilt with it.
  const focused = document.activeElement && box.contains(document.activeElement) ? document.activeElement.id : '';
  box.replaceChildren(...cards);
  if (focused && $(focused)) $(focused).focus();
}

function questionCard(q, queued) {
  const id = 'answer-' + q.id;
  const ta = el('textarea', { id, rows: '2', placeholder: 'Your answer', 'aria-label': 'Your answer' });
  ta.value = dec.answer[q.id] || '';
  ta.addEventListener('input', () => { dec.answer[q.id] = ta.value; });
  const send = async () => {
    const text = ta.value.trim();
    if (!text) { ta.focus(); return; }
    try {
      await act('Answered', 'question_answer', { id: q.id, text });
      delete dec.answer[q.id];
    } catch { /* told */ }
  };
  ta.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) { ev.preventDefault(); send(); }
  });
  return el('article', { class: 'card question', 'data-question': String(q.id) },
    el('h3', { text: `${who(q.agent)} asks you` + (q.priority && q.priority !== 'normal' ? ` · ${q.priority}` : '') }),
    q.from ? el('p', { class: 'sub', text: `for ${who(q.from)}` }) : null,
    el('p', { class: 'body', text: q.body }),
    el('div', { class: 'answer' }, ta, el('button', { type: 'button', class: 'primary', onclick: send }, 'Answer')),
    queued.length ? el('details', { class: 'queue' },
      el('summary', { text: queued.length === 1 ? '1 more question waits' : `${queued.length} more questions wait` }),
      el('ol', {}, ...queued.map((x) => el('li', { text: x.body })))) : null,
    el('p', { class: 'sub', text: 'The tower gets your answer as your message, and the next question opens.' }));
}

function spawnCard(r) {
  const decide = (verdict) => act(verdict === 'accept' ? 'Accepted' : 'Declined', 'request_decide', { id: r.id, verdict }).catch(() => {});
  const where = [r.repo && `repo ${r.repo}`, r.branch && `branch ${r.branch}`, r.profile && `profile ${r.profile}`].filter(Boolean).join(', ');
  return el('article', { class: 'card spawn', 'data-request': 's' + r.id },
    el('h3', { text: `s${r.id} · ${who(r.from)} asks for a ${r.role} agent` }),
    el('div', { class: 'row' },
      el('button', { type: 'button', class: 'allow', onclick: () => decide('accept') }, 'Accept'),
      el('button', { type: 'button', onclick: () => decide('decline') }, 'Decline')),
    where ? el('p', { class: 'sub', text: where }) : null,
    r.reason ? el('p', { class: 'body', text: 'Why: ' + r.reason }) : null,
    el('details', {}, el('summary', { text: 'Its brief' }), el('pre', { class: 'what', text: r.brief + (r.size > r.brief.length ? ' …' : '') })),
    el('p', { class: 'sub', text: 'One-off only: a spawn request has no durable form (lea approve s' + r.id + ' / lea deny s' + r.id + ').' }));
}

function grantLine(g) {
  const decide = (verdict) => act(verdict === 'approve' ? 'Approved' : 'Denied', 'grant_request_decide', { id: g.id, verdict }).catch(() => {});
  const what = g.capability ? `${g.capability} (${g.does})` : g.action;
  const scope = [g.repo && `on ${g.repo}` + (g.github ? ` (${g.github})` : ''), g.branch && g.branch !== '*' && `branch ${g.branch}`,
    g.tag && `tags ${g.tag}`, g.ttl_s && `for ${lasting(g.ttl_s)}`].filter(Boolean).join(', ');
  return el('li', { 'data-request': 'g' + g.id },
    el('div', { class: 'line' },
      el('span', { class: 'what-g', text: `g${g.id} · ${who(g.from)} and its children: ${what}` }),
      el('span', { class: 'row' },
        el('button', { type: 'button', class: 'allow', onclick: () => decide('approve') }, 'Approve'),
        el('button', { type: 'button', onclick: () => decide('deny') }, 'Deny'))),
    scope ? el('span', { class: 'sub', text: scope }) : null,
    g.reason ? el('span', { class: 'sub', text: 'Why: ' + g.reason }) : null);
}

function grantCard(gs, advice) {
  const all = async () => {
    for (const g of gs) {
      try { await api('grant_request_decide', { id: g.id, verdict: 'approve' }); } catch (e) { toast(`g${g.id}: ${e.message}`, true); return; }
    }
    toast(gs.length === 1 ? 'Approved' : `Approved ${gs.length} requests`);
  };
  const a = advice || {};
  return el('article', { class: 'card grants' },
    el('h3', { text: gs.length === 1 ? 'A lead asks for a grant' : `Leads ask for ${gs.length} grants` }),
    el('ol', { class: 'glist' }, ...gs.map(grantLine)),
    el('div', { class: 'advice' },
      a.one_off ? el('p', { class: 'one-off', text: a.one_off }) : null,
      a.durable ? el('p', { class: 'durable', text: a.durable }) : null,
      a.not_covered ? el('p', { class: 'not-covered', text: a.not_covered }) : null),
    el('div', { class: 'row' },
      gs.length > 1 ? el('button', { type: 'button', class: 'allow', onclick: all }, `Approve all ${gs.length}`) : null,
      a.command ? el('button', { type: 'button', onclick: () => openAllowances(a), title: a.command }, 'Allow durably…') : null));
}

// --- allowances -----------------------------------------------------------------------------

function setText(s, now) {
  const active = (s.Grants || []).filter((g) => !g.Revoked && g.Expires > now);
  const caps = s.Capabilities || [];
  const repos = [...new Set(active.filter((g) => g.Repo).map((g) => `${g.Repo} (${g.Pin})`))];
  const g0 = active[0] || {};
  const tag = (active.find((g) => g.Action === 'release-create' && g.Tag) || {}).Tag;
  const parts = [caps.join(' ')];
  if (repos.length) parts.push('on ' + repos.join(', '));
  if (g0.Branch && g0.Branch !== '*') parts.push('branches ' + g0.Branch);
  if (g0.Agent) parts.push('for ' + who(g0.Agent) + ' and its children');
  if (tag) parts.push('tags ' + tag);
  parts.push(s.Expires >= 2 ** 62 - 1 ? 'until revoked' : 'until ' + new Date(s.Expires * 1000).toLocaleString());
  return parts.join(', ');
}

async function loadAllowances() {
  let r;
  try { r = await api('allow_list'); } catch (e) { $('allow-err').textContent = e.message; return; }
  const v = r.allowances || {};
  const sets = v.sets || [];
  const now = v.now || Math.floor(Date.now() / 1000);
  const off = (s) => act(`Allowance #${s.ID} revoked`, 'allow_off', { set: s.ID }).then(loadAllowances).catch(() => {});
  $('allow-sets').replaceChildren(...(sets.length ? sets.map((s) => el('li', { 'data-set': String(s.ID) },
    el('span', { class: 'line' }, el('span', { text: `#${s.ID} · ${setText(s, now)}` }),
      el('button', { type: 'button', class: 'danger', onclick: () => off(s), title: `lea allow off ${s.ID}` }, 'Revoke')),
    el('span', { class: 'sub', text: `Undo on the host: lea allow off ${s.ID}` + (v.last_use && v.last_use[s.ID] ? ` · last used ${ago(v.last_use[s.ID])}` : '') })))
    : [el('li', { class: 'sub', text: 'No allowance: agents ask as usual.' })]));
  if (!dec.allowCaps) {
    dec.allowCaps = r.capabilities || [];
    const box = $('allow-caps');
    const opt = (name, does) => el('label', { class: 'cap' }, el('input', { type: 'checkbox', name: 'cap', value: name }), el('span', {},
      el('b', { text: name }), ' ' + does));
    const presets = Object.entries(r.presets || {}).map(([p, xs]) => opt(p, `(${xs.join(' + ')})`));
    box.append(...presets, ...dec.allowCaps.map((c) => opt(c.name, c.does)));
  }
}

function allowAsk() {
  const f = $('form-allow');
  const split = (v) => v.split(/[\s,]+/).filter(Boolean);
  return {
    capabilities: [...f.querySelectorAll('input[name=cap]:checked')].map((i) => i.value),
    repos: split(f.repos.value), tag: f.tag.value.trim(), branch: f.branch.value.trim(), agent: f.agent.value.trim(), for: f.for.value.trim(),
  };
}

function resetPreview() {
  dec.allowPreview = null;
  $('allow-preview').hidden = true;
  $('allow-ok').disabled = true;
}

async function previewAllowance() {
  $('allow-err').textContent = '';
  resetPreview();
  const ask = allowAsk();
  let p;
  try { p = await api('allow_preview', ask); } catch (e) { $('allow-err').textContent = e.message; return; }
  dec.allowPreview = { ask, digest: p.digest };
  const pins = (p.repos || []).map((r) => el('li', { class: r.new || r.was ? 'unseen' : '' },
    `${r.repo} → ${r.pin}`, r.new ? ' (new pin: check it)' : '', r.was ? ` (was ${r.was}: check it)` : ''));
  $('allow-preview').replaceChildren(
    el('code', { text: p.command }),
    pins.length ? el('ul', {}, ...pins) : null,
    (p.skipped || []).length ? el('p', { class: 'sub', text: 'Left out: ' + p.skipped.join('; ') }) : null,
    p.unseen ? el('p', { class: 'warn', text: 'Agents can rewrite .git/config: record only pins you recognise.' }) : null);
  $('allow-preview').hidden = false;
  $('allow-ok').disabled = false;
}

async function recordAllowance() {
  if (!dec.allowPreview) return;
  const { ask, digest } = dec.allowPreview;
  $('allow-ok').disabled = true;
  try {
    const r = await api('allow_add', { ...ask, digest });
    toast(`Allowance #${r.set.ID} recorded` + (r.unchanged ? ' (unchanged)' : ''));
    resetPreview();
    for (const i of $('form-allow').querySelectorAll('input[name=cap]')) i.checked = false;
    loadAllowances();
  } catch (e) {
    $('allow-err').textContent = e.message;
    $('allow-ok').disabled = false;
  }
}

// openAllowances shows the allowances; with an advice, its lea allow is filled in.
async function openAllowances(advice) {
  const d = $('dlg-allow');
  const f = $('form-allow');
  $('allow-err').textContent = '';
  resetPreview();
  if (!d.open) d.showModal();
  await loadAllowances();
  if (advice && advice.capabilities) {
    const want = new Set(advice.capabilities);
    for (const i of f.querySelectorAll('input[name=cap]')) i.checked = want.has(i.value);
    f.repos.value = (advice.repos || []).join(' ');
    f.tag.value = advice.tag || '';
    f.branch.value = f.agent.value = f.for.value = '';
    previewAllowance();
  }
}

$('form-allow').addEventListener('input', resetPreview);
$('allow-check').addEventListener('click', previewAllowance);
$('allow-ok').addEventListener('click', recordAllowance);
$('btn-allow').addEventListener('click', () => openAllowances());

// --- egress ---------------------------------------------------------------------------------

async function loadEgress() {
  const list = $('egress-list');
  let rs;
  try { rs = await api('egress_refused'); } catch (e) {
    list.replaceChildren(el('li', { class: 'sub', text: e.message }));
    return;
  }
  list.replaceChildren(...(rs.length ? rs.map((r) => el('li', { 'data-host': r.host },
    el('span', { class: 'line' }, el('b', { text: r.host }),
      r.allowed_by ? el('span', { class: 'sub', text: 'allowed now: ' + r.allowed_by })
        : el('button', { type: 'button', class: 'allow', onclick: () => egress(true, r.host) }, 'Allow')),
    el('span', { class: 'sub', text: `refused ${r.count}× · last ${ago(r.last)} · from ${(r.from || []).join(', ')}` })))
    : [el('li', { class: 'sub', text: 'No host refused in the last 24 h.' })]));
}

async function egress(allow, host) {
  host = (host || $('egress-host').value).trim().toLowerCase();
  $('egress-err').textContent = '';
  if (!host) { $('egress-host').focus(); return; }
  try {
    const r = await api(allow ? 'egress_allow' : 'egress_deny', { host });
    $('egress-said').textContent = r.said || (allow ? `${host} allowed` : `${host} no longer allowed`);
    $('egress-said').hidden = false;
    toast(allow ? `${host} allowed, live` : `${host} denied, live`);
    loadEgress();
  } catch (e) {
    $('egress-err').textContent = e.message;
  }
}

function openEgress() {
  $('egress-err').textContent = '';
  $('egress-said').hidden = true;
  if (!$('dlg-egress').open) $('dlg-egress').showModal();
  loadEgress();
}

$('egress-allow').addEventListener('click', () => egress(true));
$('egress-deny').addEventListener('click', () => egress(false));
$('egress-host').addEventListener('keydown', (ev) => { if (ev.key === 'Enter') { ev.preventDefault(); egress(true); } });
$('btn-egress').addEventListener('click', openEgress);

if (ui.state) renderDecisions();
