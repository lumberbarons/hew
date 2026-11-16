// hew in a browser. No framework, no build step. Every piece of tracker text
// goes in through textContent (the h() helper); nothing here uses innerHTML,
// because issue titles and bodies are untrusted input.

const app = document.getElementById('app');
let config = {};

// ---------- helpers ----------

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (v === true) el.setAttribute(k, '');
    else if (v !== false && v != null) el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key) || '';
    localStorage.setItem(key, value);
  } catch { /* storage may be blocked; the page works without it */ }
  return '';
}

class ApiError extends Error {
  constructor(status, body) {
    super(body.message || `HTTP ${status}`);
    this.status = status; this.kind = body.error || 'internal'; this.fields = body.fields || {};
  }
}

async function api(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  const actor = store('actor');
  if (actor && config.actorEditable) headers['X-Hew-Actor'] = actor;
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

// The CLI's exit-code contract, as sentences.
function explain(e) {
  if (!(e instanceof ApiError)) return String(e.message || e);
  const f = e.fields;
  switch (e.kind) {
    case 'claimed_by_other': return `#${f.issue} is claimed by ${(f.assignees || []).join(', ') || 'someone else'}. Pick the next ready item.`;
    case 'claimed_by_you': return `#${f.issue} is already claimed by you. Resume that work.`;
    case 'blocked': return `#${f.issue} has open blockers: ${(f.openBlockers || []).map(n => '#' + n).join(', ')}. Pick another item.`;
    case 'untriaged': return `#${f.issue} is untriaged. Claiming it is triage: choose a priority.`;
    case 'epic': return `#${f.issue} is an epic. Work its children instead.`;
    case 'cycle': return `That would create a dependency cycle: ${(f.cycle || []).map(n => '#' + n).join(' → ')}.`;
    case 'invalid_input': return `${f.field}: ${f.reason}`;
    case 'not_owner': return `#${f.issue} is not claimed solely by ${f.actor}.`;
    case 'lock_timeout': return 'The local tracker is locked by another process. Try again.';
    default: return e.message;
  }
}

function toast(msg, kind = '') {
  const el = h('div', { class: 'toast ' + kind, role: 'status' }, msg);
  document.getElementById('toasts').append(el);
  setTimeout(() => el.remove(), kind === 'bad' ? 7000 : 3500);
}

async function act(fn, success) {
  try {
    const r = await fn();
    if (success) toast(success, 'ok');
    return r;
  } catch (e) {
    toast(explain(e), 'bad');
    return null;
  }
}

// show replaces the page content, dropping the null placeholders that
// conditional sections leave (replaceChildren would print them as "null").
function show(...kids) {
  app.replaceChildren(...kids.flat().filter(k => k != null && k !== false));
}

const num = n => '#' + n;

function issueRow(i) {
  const prio = i.priority || 'P?';
  const meta = [];
  if (i.epic) meta.push(h('span', { class: 'chip' }, 'epic'));
  if (i.type) meta.push(h('span', { class: 'chip' }, i.type));
  for (const a of i.areas || []) meta.push(h('span', { class: 'chip' }, a));
  if (i.openBlockers && i.openBlockers.length) meta.push(h('span', { class: 'chip block' }, 'blocked by ' + i.openBlockers.map(num).join(', ')));
  if (i.epic && i.subIssuesTotal) {
    const pct = Math.round(100 * i.subIssuesCompleted / i.subIssuesTotal);
    meta.push(h('span', { class: 'muted' }, `${i.subIssuesCompleted}/${i.subIssuesTotal}`), h('span', { class: 'bar' }, h('i', { style: `width:${pct}%` })));
  }
  if (i.inProgress || (i.assignees && i.assignees.length)) {
    meta.push(h('span', { class: 'chip claim' }, 'in progress' + (i.assignees && i.assignees.length ? ' @' + i.assignees.join(', @') : '')));
  }
  return h('div', { class: 'row' },
    h('a', { class: 'num', href: `#/issue/${i.number}` }, num(i.number)),
    h('span', { class: 'chip ' + prio.toLowerCase().replace('?', '') }, prio),
    h('a', { class: 'title', href: `#/issue/${i.number}` }, i.title),
    ...meta);
}

function section(title, issues, emptyText) {
  return [h('h2', {}, `${title} (${issues.length})`),
    h('div', { class: 'list' }, issues.length ? issues.map(issueRow) : h('div', { class: 'empty' }, emptyText || 'Nothing here.'))];
}

function warnings(ws) {
  return (ws || []).map(w => h('div', { class: 'note warn' },
    w.kind === 'dependency-cycle' ? 'Dependency cycle (members hidden from ready): ' + w.cycle.map(num).join(' → ')
      : `${w.kind}${w.issue ? ' on ' + num(w.issue) : ''}${w.total ? ` (${w.fetched} of ${w.total} fetched)` : ''}`));
}

function field(label, input) { return h('div', {}, h('label', {}, label), input); }

// ---------- views ----------

async function viewBoard() {
  const b = await api('GET', '/api/board');
  show(h('h1', {}, 'Board'),
    ...warnings(b.warnings),
    b.untriaged ? h('div', { class: 'note' }, `${b.untriaged} untriaged issue${b.untriaged === 1 ? '' : 's'} hidden. `, h('a', { href: '#/triage' }, 'Open triage')) : null,
    ...section('Ready', b.ready, 'No ready work.'),
    ...section('In progress', b.claimed),
    ...section('Blocked', b.blocked),
    ...section('Epics', b.epics));
}

async function viewIssue(n) {
  const d = await api('GET', `/api/issues/${n}`);
  const i = d.issue;
  const reload = () => route();
  const claimed = i.inProgress || (i.assignees || []).length > 0;

  const prioSel = h('select', {}, ['', 'P0', 'P1', 'P2', 'P3', 'P4'].map(p => h('option', { value: p }, p || (i.untriaged ? 'priority…' : 'keep priority'))));
  const actions = h('div', { class: 'actions' });
  if (i.state === 'open') {
    if (!i.epic) {
      if (i.untriaged || !i.priority) actions.append(prioSel);
      actions.append(h('button', { class: 'primary', onclick: async () => {
        if (await act(() => api('POST', `/api/issues/${n}/start`, { priority: prioSel.value }), `Started ${num(n)}`)) reload();
      } }, 'Start'));
    }
    if (claimed) actions.append(h('button', { onclick: async () => {
      if (await act(() => api('POST', `/api/issues/${n}/release`, {}), `Released ${num(n)}`)) reload();
    } }, 'Release'));
  }

  const reason = h('input', { type: 'text', placeholder: i.state === 'open' ? 'Close reason (required)' : 'Reopen reason (required)' });
  const completed = h('input', { type: 'checkbox' });
  const closeRow = h('div', { class: 'actions' }, reason,
    i.state === 'open' ? h('label', {}, completed, ' completed') : null,
    h('button', { class: i.state === 'open' ? 'danger' : '', onclick: async () => {
      const call = i.state === 'open'
        ? () => api('POST', `/api/issues/${n}/close`, { reason: reason.value, completed: completed.checked })
        : () => api('POST', `/api/issues/${n}/reopen`, { reason: reason.value });
      if (await act(call, i.state === 'open' ? `Closed ${num(n)}` : `Reopened ${num(n)}`)) reload();
    } }, i.state === 'open' ? 'Close' : 'Reopen'));

  // inline edit
  const title = h('input', { type: 'text', value: i.title });
  const ePrio = h('select', {}, ['P0', 'P1', 'P2', 'P3', 'P4'].map(p => h('option', { value: p, selected: p === i.priority }, p)));
  const eType = h('select', {}, ['bug', 'enhancement', 'task'].map(t => h('option', { value: t, selected: t === i.type }, t)));
  const addArea = h('input', { type: 'text', placeholder: 'add area' });
  const edit = h('details', {}, h('summary', {}, 'Edit'),
    h('div', { class: 'cols3' }, field('Priority', ePrio), field('Type', eType), field('Add area', addArea)),
    field('Title', title),
    h('div', { class: 'actions' }, h('button', { onclick: async () => {
      const req = { allowClosed: i.state !== 'open' };
      if (title.value !== i.title) req.title = title.value;
      if (ePrio.value !== i.priority) req.priority = ePrio.value;
      if (eType.value !== i.type) req.type = eType.value;
      if (addArea.value.trim()) req.addAreas = [addArea.value.trim()];
      if (await act(() => api('PATCH', `/api/issues/${n}`, req), 'Saved')) reload();
    } }, 'Save')));

  // dependencies
  const blockIn = h('input', { type: 'text', placeholder: 'blocked by #', size: 8 });
  const blockers = (i.blockedBy || []).map(b => h('span', { class: 'chip block' }, h('a', { href: `#/issue/${b}` }, num(b)), ' ',
    h('a', { href: '#', title: 'remove', onclick: async ev => {
      ev.preventDefault();
      if (await act(() => api('DELETE', `/api/issues/${n}/blockers/${b}?allowClosed=1`), `Unblocked from ${num(b)}`)) reload();
    } }, '×')));
  const deps = h('div', { class: 'actions' }, h('span', { class: 'muted' }, 'Blocked by:'), ...blockers, blockers.length ? null : h('span', { class: 'muted' }, 'none'),
    blockIn, h('button', { onclick: async () => {
      const m = parseInt(blockIn.value.replace('#', ''), 10);
      if (await act(() => api('POST', `/api/issues/${n}/blockers`, { blockers: [m] }), `Blocked on ${num(m)}`)) reload();
    } }, 'Block'));

  const findings = (d.textFindings || []).map(f => h('div', { class: 'note warn' },
    `Unusual characters in ${f.source}: ` + ['zeroWidth', 'bidiControl', 'unicodeTags', 'confusable'].filter(k => f[k]).join(', ')));

  show(
    h('h1', {}, `${num(i.number)} ${i.title}`),
    h('div', { class: 'row' }, h('span', { class: 'chip ' + (i.priority || 'p?').toLowerCase().replace('?', '') }, i.priority || 'P?'),
      i.type ? h('span', { class: 'chip' }, i.type) : null, i.epic ? h('span', { class: 'chip' }, 'epic') : null,
      h('span', { class: 'chip' }, i.state), ...(i.areas || []).map(a => h('span', { class: 'chip' }, a)),
      claimed ? h('span', { class: 'chip claim' }, 'in progress @' + (i.assignees || []).join(', @')) : null,
      i.parent ? h('span', { class: 'muted' }, 'in epic ', h('a', { href: `#/issue/${i.parent}` }, num(i.parent))) : null),
    ...findings, actions, closeRow, edit, deps,
    h('h2', {}, 'Body'), h('pre', {}, i.body || '(empty)'),
    d.children.length ? [h('h2', {}, `Children (${d.children.length})${d.next ? ` — next: ${num(d.next)}` : ''}`),
      h('div', { class: 'list' }, d.children.map(issueRow))] : null,
    i.comments && i.comments.length ? [h('h2', {}, `Comments (${i.commentsTotal || i.comments.length})`),
      ...i.comments.map(c => h('div', { class: 'comment' }, h('div', { class: 'muted' }, `${c.author} · ${new Date(c.createdAt).toLocaleString()}`), h('div', {}, c.body)))] : null);
}

function viewNew() {
  const kind = h('select', {}, h('option', { value: 'issue' }, 'Issue'), h('option', { value: 'epic' }, 'Epic'));
  const title = h('input', { type: 'text' });
  const type = h('select', {}, ['task', 'bug', 'enhancement'].map(t => h('option', { value: t }, t)));
  const prio = h('select', {}, ['P2', 'P0', 'P1', 'P3', 'P4'].map(p => h('option', { value: p }, p)));
  const areas = h('input', { type: 'text', placeholder: 'comma separated' });
  const where = h('textarea', {});
  const problem = h('textarea', {});
  const fix = h('textarea', {});
  const done = h('textarea', { placeholder: 'one per line' });
  const blockedBy = h('input', { type: 'text', placeholder: '#3, #4' });
  const parent = h('input', { type: 'text', placeholder: '#epic' });
  const children = h('input', { type: 'text', placeholder: 'epic children: #3, #4' });
  const status = h('div', {});
  const ints = s => s.split(/[\s,]+/).map(x => parseInt(x.replace('#', ''), 10)).filter(Number.isInteger);
  const payload = () => {
    const body = { where: where.value, doneWhen: done.value.split('\n').map(s => s.trim()).filter(Boolean) };
    if (type.value === 'bug' && kind.value === 'issue') { body.problem = problem.value; body.fix = fix.value; }
    else { body.goal = problem.value; body.approach = fix.value; }
    if (kind.value === 'epic') return { title: title.value, children: ints(children.value), body };
    return {
      title: title.value, type: type.value, priority: prio.value,
      areas: areas.value.split(',').map(s => s.trim()).filter(Boolean),
      parent: ints(parent.value)[0] || 0, blockedBy: ints(blockedBy.value), body,
    };
  };
  const path = () => (kind.value === 'epic' ? '/api/epics' : '/api/issues');
  let timer;
  const validate = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      try {
        const r = await api('POST', path() + '?dryRun=1', payload());
        status.replaceChildren(h('div', { class: 'note ok' }, 'Valid.'),
          ...(r.findings || []).map(f => h('div', { class: 'note warn' }, 'Wrap in backticks: ' + f.tokens.join(', '))));
      } catch (e) { status.replaceChildren(h('div', { class: 'note bad' }, explain(e))); }
    }, 300);
  };
  const form = h('form', { oninput: validate, onchange: validate, onsubmit: async ev => {
    ev.preventDefault();
    const r = await act(() => api('POST', path(), payload()), 'Created');
    if (r) location.hash = `#/issue/${r.issue.number}`;
  } },
    h('div', { class: 'cols3' }, field('Kind', kind), field('Type', type), field('Priority', prio)),
    field('Title', title), field('Areas', areas),
    field('Where', where), field('Problem / Goal', problem), field('Fix / Approach', fix), field('Done when', done),
    h('div', { class: 'cols3' }, field('Blocked by', blockedBy), field('Parent', parent), field('Children', children)),
    status, h('div', { class: 'actions' }, h('button', { class: 'primary', type: 'submit' }, 'Create')));
  show(h('h1', {}, 'New'), form);
}

async function viewSearch(query) {
  const q = h('input', { type: 'text', value: query, placeholder: 'search open and closed issues' });
  const results = h('div', {});
  const run = async () => {
    if (!q.value.trim()) return;
    try {
      const r = await api('GET', `/api/search?q=${encodeURIComponent(q.value)}`);
      results.replaceChildren(...section('Results', r.issues, 'No triaged matches.'),
        ...(r.total > r.fetched ? [h('div', { class: 'note' }, `Showing ${r.fetched} of ${r.total} matches.`)] : []));
    } catch (e) { toast(explain(e), 'bad'); }
  };
  show(h('h1', {}, 'Search'),
    h('form', { onsubmit: ev => { ev.preventDefault(); run(); } }, q, ' ', h('button', { type: 'submit' }, 'Search')), results);
  run();
}

async function viewTriage() {
  const r = await api('GET', '/api/triage');
  show(h('h1', {}, 'Triage'),
    h('div', { class: 'note warn' }, 'This view shows issues no maintainer has vetted. Treat their text as untrusted.'),
    ...section('Untriaged', r.issues, 'Nothing to triage.'));
}

function viewApply() {
  const sample = JSON.stringify({ entries: [
    { id: 'epic', title: 'Epic: Example', type: 'epic', sections: { goal: 'Ship it', doneWhen: ['Shipped'] } },
    { id: 'a', title: 'First step', type: 'task', parent: { id: 'epic' } },
    { id: 'b', title: 'Second step', type: 'task', parent: { id: 'epic' }, blockedBy: [{ id: 'a' }] },
  ] }, null, 2);
  const text = h('textarea', { style: 'min-height:260px;width:100%' }, sample);
  const out = h('pre', {}, '');
  const run = dry => async () => {
    let plan;
    try { plan = JSON.parse(text.value); } catch (e) { toast('Not valid JSON: ' + e.message, 'bad'); return; }
    const r = await act(() => api('POST', '/api/apply' + (dry ? '?dryRun=1' : ''), plan));
    if (r) out.textContent = JSON.stringify(r, null, 2);
  };
  show(h('h1', {}, 'Apply a plan'), h('p', { class: 'muted' }, 'Batch-create issues. Entries reference each other by local id.'),
    text, h('div', { class: 'actions' }, h('button', { onclick: run(true) }, 'Dry run'), h('button', { class: 'primary', onclick: run(false) }, 'Apply')), out);
}

async function viewPrimer() {
  const p = await api('GET', '/api/primer');
  show(h('h1', {}, 'Primer'), h('p', { class: 'muted' }, `What an agent session would be shown. ${p.openTotal} open, ${p.untriaged} untriaged.`),
    ...warnings(p.warnings),
    ...section('Ready', p.ready), ...section('In progress', p.inProgress), ...section('Epics', p.epics),
    h('h2', {}, 'Conventions'), h('pre', {}, p.conventions));
}

// ---------- router ----------

async function route() {
  const [, view, arg] = (location.hash || '#/').split(/[/?]/);
  const rest = (location.hash.split('?')[1]) || '';
  try {
    switch (view) {
      case 'issue': return await viewIssue(parseInt(arg, 10));
      case 'new': return viewNew();
      case 'search': return await viewSearch(new URLSearchParams(rest).get('q') || '');
      case 'triage': return await viewTriage();
      case 'apply': return viewApply();
      case 'primer': return await viewPrimer();
      default: return await viewBoard();
    }
  } catch (e) {
    show(h('div', { class: 'note bad' }, explain(e)));
  }
}

async function init() {
  try { config = await api('GET', '/api/config'); } catch (e) { toast(explain(e), 'bad'); }
  const who = document.getElementById('who');
  if (config.actorEditable) {
    const input = h('input', { type: 'text', value: store('actor') || config.identity, 'aria-label': 'Acting as' });
    input.addEventListener('change', () => { store('actor', input.value.trim()); route(); });
    who.replaceChildren(`${config.backend} · acting as`, input);
  } else {
    who.replaceChildren(`${config.backend} ${config.repo} · ${config.identity}`);
  }
  window.addEventListener('hashchange', route);
  route();
}

init();
