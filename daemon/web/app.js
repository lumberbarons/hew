// hew in a browser. No framework, no build step. Every piece of tracker text
// goes in through textContent (the h() helper); nothing here uses innerHTML,
// because issue titles and bodies are untrusted input.

import { renderMarkdown } from './markdown.js';

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

function chip(text, cls) { return h('span', { class: 'chip' + (cls ? ' ' + cls : '') }, text); }
const prioClass = p => 'p' + (p || '?').slice(1).replace('?', '');

function issueRow(i) {
  const prio = i.priority || 'P?';
  const meta = [];
  if (i.epic) meta.push(chip('epic'));
  if (i.type) meta.push(chip(i.type));
  for (const a of i.areas || []) meta.push(chip(a));
  if (i.openBlockers && i.openBlockers.length) meta.push(chip('blocked by ' + i.openBlockers.map(num).join(', '), 'block'));
  if (i.epic && i.subIssuesTotal) {
    const pct = Math.round(100 * i.subIssuesCompleted / i.subIssuesTotal);
    meta.push(h('span', { class: 'progress' }, `${i.subIssuesCompleted}/${i.subIssuesTotal}`, h('span', { class: 'bar' }, h('i', { style: `width:${pct}%` }))));
  }
  if (i.inProgress || (i.assignees && i.assignees.length)) {
    meta.push(chip('in progress' + (i.assignees && i.assignees.length ? ' @' + i.assignees.join(', @') : ''), 'claim'));
  }
  return h('div', { class: 'row' },
    h('a', { class: 'num', href: `#/issue/${i.number}` }, num(i.number)),
    chip(prio, prioClass(i.priority)),
    h('a', { class: 'title', href: `#/issue/${i.number}` }, i.title),
    meta.length ? h('div', { class: 'meta' }, ...meta) : null);
}

function section(title, issues, emptyText) {
  return [h('h2', {}, title, h('span', { class: 'count' }, ` · ${issues.length}`)),
    h('div', { class: 'list' }, issues.length ? issues.map(issueRow) : h('div', { class: 'empty' }, emptyText || 'Nothing here.'))];
}

function warnings(ws) {
  return (ws || []).map(w => h('div', { class: 'note warn' },
    w.kind === 'dependency-cycle' ? 'Dependency cycle (members hidden from ready): ' + w.cycle.map(num).join(' → ')
      : `${w.kind}${w.issue ? ' on ' + num(w.issue) : ''}${w.total ? ` (${w.fetched} of ${w.total} fetched)` : ''}`));
}

let uid = 0;
// field wraps a control in a labelled block; the label is tied to the control
// by id so clicking it focuses the input and screen readers announce it.
function field(label, input, hint) {
  const id = 'f' + (++uid);
  input.id = id;
  return h('div', { class: 'field' }, h('label', { for: id }, label), input, hint ? h('p', { class: 'hint' }, hint) : null);
}

function formSection(title, sub, ...kids) {
  return h('div', { class: 'form-section' }, h('h3', {}, title, sub ? h('small', {}, sub) : null), ...kids);
}

function pageHead(title, sub) {
  return h('div', { class: 'page-head' }, h('h1', {}, title), sub ? h('span', { class: 'sub' }, sub) : null);
}

// ---------- views ----------

async function viewBoard() {
  const b = await api('GET', '/api/board');
  show(pageHead('Board'),
    ...warnings(b.warnings),
    b.untriaged ? h('div', { class: 'note' }, `${b.untriaged} untriaged issue${b.untriaged === 1 ? '' : 's'} hidden. `, h('a', { href: '#/triage' }, 'Open triage')) : null,
    ...section('Epics', b.epics),
    ...section('Ready', b.ready, 'No ready work.'),
    ...section('In progress', b.claimed),
    ...section('Blocked', b.blocked));
}

async function viewIssue(n) {
  const d = await api('GET', `/api/issues/${n}`);
  const i = d.issue;
  const reload = () => route();
  const open = i.state === 'open';
  const claimed = i.inProgress || (i.assignees || []).length > 0;

  // claim
  const prioSel = h('select', { 'aria-label': 'Priority', style: 'width:auto' },
    ['', 'P0', 'P1', 'P2', 'P3', 'P4'].map(p => h('option', { value: p }, p || (i.untriaged ? 'Choose priority…' : 'Keep priority'))));
  const claim = h('div', { class: 'toolbar' });
  if (open && !i.epic) {
    if (i.untriaged || !i.priority) claim.append(prioSel);
    claim.append(h('button', { class: 'primary', onclick: async () => {
      if (await act(() => api('POST', `/api/issues/${n}/start`, { priority: prioSel.value }), `Started ${num(n)}`)) reload();
    } }, 'Start'));
  }
  if (open && claimed) claim.append(h('button', { onclick: async () => {
    if (await act(() => api('POST', `/api/issues/${n}/release`, {}), `Released ${num(n)}`)) reload();
  } }, 'Release'));
  if (open && i.epic) claim.append(h('span', { class: 'muted' }, 'Epics are containers. Work their children instead.'));

  // close / reopen
  const reason = h('input', { type: 'text', 'aria-label': 'Reason', placeholder: open ? 'Reason for closing (required)' : 'Reason for reopening (required)' });
  const completed = h('input', { type: 'checkbox' });
  const closer = h('div', { class: 'toolbar' }, reason,
    open ? h('label', { class: 'check' }, completed, 'Completed') : null,
    h('button', { class: open ? 'danger' : '', onclick: async () => {
      const call = open
        ? () => api('POST', `/api/issues/${n}/close`, { reason: reason.value, completed: completed.checked })
        : () => api('POST', `/api/issues/${n}/reopen`, { reason: reason.value });
      if (await act(call, open ? `Closed ${num(n)}` : `Reopened ${num(n)}`)) reload();
    } }, open ? 'Close issue' : 'Reopen issue'));

  // dependencies
  const blockIn = h('input', { type: 'text', 'aria-label': 'Blocked by issue number', placeholder: 'Blocked by #…', style: 'max-width:200px' });
  const blockers = (i.blockedBy || []).map(b => chip([h('a', { href: `#/issue/${b}` }, num(b)),
    h('a', { href: '#', title: 'Remove', 'aria-label': `Remove blocker ${num(b)}`, onclick: async ev => {
      ev.preventDefault();
      if (await act(() => api('DELETE', `/api/issues/${n}/blockers/${b}?allowClosed=1`), `Unblocked from ${num(b)}`)) reload();
    } }, '×')].flat(), 'block'));
  const deps = h('div', { class: 'toolbar' },
    ...(blockers.length ? blockers : [h('span', { class: 'muted' }, 'Nothing')]),
    blockIn, h('button', { onclick: async () => {
      const m = parseInt(blockIn.value.replace('#', ''), 10);
      if (await act(() => api('POST', `/api/issues/${n}/blockers`, { blockers: [m] }), `Blocked on ${num(m)}`)) reload();
    } }, 'Add blocker'));

  // edit
  const title = h('input', { type: 'text', value: i.title });
  const ePrio = h('select', {}, ['P0', 'P1', 'P2', 'P3', 'P4'].map(p => h('option', { value: p, selected: p === i.priority }, p)));
  const eType = h('select', {}, ['bug', 'enhancement', 'task'].map(t => h('option', { value: t, selected: t === i.type }, t)));
  const addArea = h('input', { type: 'text', placeholder: 'e.g. cli' });
  const edit = h('details', { class: 'card panel' }, h('summary', {}, 'Edit details'),
    field('Title', title),
    h('div', { class: 'grid-3' }, field('Priority', ePrio), field('Type', eType), field('Add area', addArea)),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: async () => {
      const req = { allowClosed: !open };
      if (title.value !== i.title) req.title = title.value;
      if (ePrio.value !== i.priority) req.priority = ePrio.value;
      if (eType.value !== i.type) req.type = eType.value;
      if (addArea.value.trim()) req.addAreas = [addArea.value.trim()];
      if (await act(() => api('PATCH', `/api/issues/${n}`, req), 'Saved')) reload();
    } }, 'Save changes')));

  const findings = (d.textFindings || []).map(f => h('div', { class: 'note warn' },
    `Unusual characters in ${f.source}: ` + ['zeroWidth', 'bidiControl', 'unicodeTags', 'confusable'].filter(k => f[k]).join(', ')));

  const prow = (label, body) => h('div', { class: 'prow' }, h('div', { class: 'label' }, label), body);
  show(
    h('div', { class: 'issue-head' }, h('h1', {}, h('span', { class: 'n' }, num(i.number)), i.title)),
    h('div', { class: 'issue-meta' }, chip(i.priority || 'P?', prioClass(i.priority)),
      i.type ? chip(i.type) : null, i.epic ? chip('epic') : null,
      chip(i.state, open ? '' : 'state-closed'), ...(i.areas || []).map(a => chip(a)),
      claimed ? chip('in progress @' + (i.assignees || []).join(', @'), 'claim') : null,
      i.parent ? h('span', { class: 'muted' }, 'in epic ', h('a', { href: `#/issue/${i.parent}` }, num(i.parent))) : null),
    ...findings,
    h('div', { class: 'card prows' },
      claim.children.length ? prow('Work', claim) : null,
      prow(open ? 'Close' : 'Reopen', closer),
      prow('Blocked by', deps)),
    edit,
    h('h2', {}, 'Body'), i.body ? renderMarkdown(i.body) : h('p', { class: 'muted' }, '(empty)'),
    d.children.length ? [h('h2', {}, 'Children', h('span', { class: 'count' }, ` · ${d.children.length}${d.next ? ` · next ${num(d.next)}` : ''}`)),
      h('div', { class: 'list' }, d.children.map(issueRow))] : null,
    i.comments && i.comments.length ? [h('h2', {}, 'Comments', h('span', { class: 'count' }, ` · ${i.commentsTotal || i.comments.length}`)),
      ...i.comments.map(c => h('div', { class: 'card comment' }, h('div', { class: 'who' }, `${c.author} · ${new Date(c.createdAt).toLocaleString()}`), renderMarkdown(c.body)))] : null);
}

function viewNew() {
  const kind = h('select', {}, h('option', { value: 'issue' }, 'Issue'), h('option', { value: 'epic' }, 'Epic'));
  const title = h('input', { type: 'text', autocomplete: 'off' });
  const type = h('select', {}, ['task', 'bug', 'enhancement'].map(t => h('option', { value: t }, t)));
  const prio = h('select', {}, ['P2', 'P0', 'P1', 'P3', 'P4'].map(p => h('option', { value: p }, p)));
  const areas = h('input', { type: 'text', placeholder: 'e.g. cli, docs' });
  const where = h('textarea', { rows: 2 });
  const problem = h('textarea', { rows: 4 });
  const fix = h('textarea', { rows: 4 });
  const done = h('textarea', { rows: 4 });
  const blockedBy = h('input', { type: 'text', placeholder: '#3, #4' });
  const parent = h('input', { type: 'text', placeholder: '#12' });
  const children = h('input', { type: 'text', placeholder: '#3, #4' });
  const status = h('div', { class: 'status', role: 'status' });

  const fTitle = field('Title', title);
  const fType = field('Type', type);
  const fPrio = field('Priority', prio);
  const fAreas = field('Areas', areas, 'Comma separated. Use sparingly: only once several issues share one.');
  const fProblem = field('Problem', problem);
  const fFix = field('Fix', fix);
  const fBlocked = field('Blocked by', blockedBy, 'Issues that must close first.');
  const fParent = field('Parent epic', parent);
  const fChildren = field('Children', children, 'Existing issues to attach as sub-issues. They must not already have a parent.');
  const relGrid = h('div', { class: 'grid-2' }, fBlocked, fParent);
  const relSection = formSection('Relationships', null, relGrid, fChildren);
  const fKind = field('Kind', kind);
  const typeGrid = h('div', { class: 'grid-3' }, fKind, fType, fPrio);

  const ints = s => s.split(/[\s,]+/).map(x => parseInt(x.replace('#', ''), 10)).filter(Number.isInteger);
  const isEpic = () => kind.value === 'epic';
  const label = (f, text) => { f.firstChild.textContent = text; };

  // Show only the fields that apply to the chosen kind and type, so the
  // form never asks for something the write path would refuse.
  const sync = () => {
    const epic = isEpic();
    for (const f of [fType, fPrio, fAreas, relGrid]) f.hidden = epic;
    fChildren.hidden = !epic;
    typeGrid.className = epic ? 'grid-2' : 'grid-3';
    const bug = !epic && type.value === 'bug';
    label(fProblem, bug ? 'Problem' : 'Goal');
    label(fFix, bug ? 'Fix' : 'Approach');
    relSection.hidden = false;
    label(fTitle, epic ? 'Title' : 'Title');
  };

  const payload = () => {
    const body = { where: where.value, doneWhen: done.value.split('\n').map(s => s.trim()).filter(Boolean) };
    if (type.value === 'bug' && !isEpic()) { body.problem = problem.value; body.fix = fix.value; }
    else { body.goal = problem.value; body.approach = fix.value; }
    if (isEpic()) return { title: title.value, children: ints(children.value), body };
    return {
      title: title.value, type: type.value, priority: prio.value,
      areas: areas.value.split(',').map(s => s.trim()).filter(Boolean),
      parent: ints(parent.value)[0] || 0, blockedBy: ints(blockedBy.value), body,
    };
  };
  const path = () => (isEpic() ? '/api/epics' : '/api/issues');
  let timer;
  const validate = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      try {
        const r = await api('POST', path() + '?dryRun=1', payload());
        status.replaceChildren(h('div', { class: 'note ok' }, 'Ready to create.'),
          ...(r.findings || []).map(f => h('div', { class: 'note warn' }, 'Wrap in backticks: ' + f.tokens.join(', '))));
      } catch (e) { status.replaceChildren(h('div', { class: 'note bad' }, explain(e))); }
    }, 300);
  };

  const create = h('button', { class: 'primary', type: 'submit' }, 'Create');
  const form = h('form', { class: 'card form', oninput: validate, onchange: () => { sync(); validate(); }, onsubmit: async ev => {
    ev.preventDefault();
    create.disabled = true;
    const r = await act(() => api('POST', path(), payload()), 'Created');
    create.disabled = false;
    if (r) location.hash = `#/issue/${r.issue.number}`;
  } },
    formSection('Basics', null, typeGrid, fTitle, fAreas),
    formSection('Description', 'Section headings are added for you. Leave out any that do not apply.',
      field('Where', where, 'Files, packages or areas involved.'), fProblem, fFix,
      field('Done when', done, 'One checklist item per line.')),
    relSection,
    h('div', { class: 'form-foot' }, status, h('button', { class: 'ghost', type: 'button', onclick: () => { location.hash = '#/'; } }, 'Cancel'), create));
  sync();
  show(pageHead('New', 'File an issue or an epic'), form);
}

async function viewSearch(query) {
  const q = h('input', { type: 'text', value: query, placeholder: 'Search open and closed issues', 'aria-label': 'Search terms', autofocus: true });
  const results = h('div', {});
  const run = async () => {
    if (!q.value.trim()) return;
    try {
      const r = await api('GET', `/api/search?q=${encodeURIComponent(q.value)}`);
      results.replaceChildren(...section('Results', r.issues, 'No triaged matches.'),
        ...(r.total > r.fetched ? [h('div', { class: 'note', style: 'margin-top:12px' }, `Showing ${r.fetched} of ${r.total} matches.`)] : []));
    } catch (e) { toast(explain(e), 'bad'); }
  };
  show(pageHead('Search'),
    h('form', { class: 'searchbar', onsubmit: ev => { ev.preventDefault(); run(); } }, q, h('button', { class: 'primary', type: 'submit' }, 'Search')), results);
  run();
}

async function viewTriage() {
  const r = await api('GET', '/api/triage');
  show(pageHead('Triage'),
    h('div', { class: 'note warn' }, 'This view shows issues no maintainer has vetted. Treat their text as untrusted.'),
    ...section('Untriaged', r.issues, 'Nothing to triage.'));
}

function viewApply() {
  const sample = JSON.stringify({ entries: [
    { id: 'epic', title: 'Epic: Example', type: 'epic', sections: { goal: 'Ship it', doneWhen: ['Shipped'] } },
    { id: 'a', title: 'First step', type: 'task', parent: { id: 'epic' } },
    { id: 'b', title: 'Second step', type: 'task', parent: { id: 'epic' }, blockedBy: [{ id: 'a' }] },
  ] }, null, 2);
  const text = h('textarea', { rows: 16, spellcheck: 'false', 'aria-label': 'Plan JSON' }, sample);
  const result = h('div', {});
  const run = dry => async () => {
    let plan;
    try { plan = JSON.parse(text.value); } catch (e) { toast('Not valid JSON: ' + e.message, 'bad'); return; }
    const r = await act(() => api('POST', '/api/apply' + (dry ? '?dryRun=1' : ''), plan));
    if (r) result.replaceChildren(h('h2', {}, dry ? 'Dry run result' : 'Result'), h('pre', {}, JSON.stringify(r, null, 2)));
  };
  show(pageHead('Apply a plan'),
    h('p', { class: 'lede' }, 'Batch-create issues from a JSON plan. Entries reference each other by local id, so a parent or blocker can be created in the same run.'),
    h('div', { class: 'card form' },
      h('div', { class: 'form-section' }, field('Plan', text)),
      h('div', { class: 'form-foot' }, h('button', { onclick: run(true) }, 'Dry run'), h('button', { class: 'primary', onclick: run(false) }, 'Apply'))),
    result);
}

async function viewPrimer() {
  const p = await api('GET', '/api/primer');
  show(pageHead('Primer'),
    h('p', { class: 'lede' }, `What an agent session would be shown. ${p.openTotal} open, ${p.untriaged} untriaged.`),
    ...warnings(p.warnings),
    ...section('Ready', p.ready), ...section('In progress', p.inProgress), ...section('Epics', p.epics),
    h('h2', {}, 'Conventions'), h('pre', {}, p.conventions));
}

// ---------- router ----------

async function route() {
  const [, view, arg] = (location.hash || '#/').split(/[/?]/);
  const rest = (location.hash.split('?')[1]) || '';
  const current = { new: '#/new', search: '#/search', triage: '#/triage', apply: '#/apply', primer: '#/primer' }[view] || '#/';
  for (const a of document.querySelectorAll('nav a')) {
    if (a.getAttribute('href') === current) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  }
  window.scrollTo(0, 0);
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
