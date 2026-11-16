// A small markdown renderer for issue bodies and comments.
//
// Issue text is untrusted, so this builds DOM nodes and never produces HTML:
// raw HTML in the source shows up as literal text, and a link is clickable
// only when its target is http(s). There is no sanitizer to get wrong because
// there is no markup string to sanitize.
//
// Supported: headings, paragraphs, fenced code, blockquotes, bullet, numbered
// and task lists (one nesting level per two-space indent), rules, and inline
// code, bold, italic, links, bare URLs and #123 issue references.

function el(tag, attrs, kids) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) node.setAttribute(k, v);
  for (const kid of kids || []) node.append(kid);
  return node;
}

const SAFE_URL = /^https?:\/\//i;

const INLINE = new RegExp([
  '`([^`]+)`', // 1 code
  '\\[([^\\]]+)\\]\\(([^)\\s]+)\\)', // 2 text, 3 url
  '\\*\\*([^*]+)\\*\\*', // 4 bold
  '__([^_]+)__', // 5 bold
  '\\*([^*\\s][^*]*)\\*', // 6 italic
  '(?<![A-Za-z0-9])_([^_\\s][^_]*)_(?![A-Za-z0-9])', // 7 italic
  '(https?://[^\\s<>)\\]]+)', // 8 bare url
  '(?<![\\w&#])#(\\d+)\\b', // 9 issue reference
].join('|'), 'g');

function link(href, kids) {
  return el('a', { href, target: '_blank', rel: 'noopener noreferrer' }, kids);
}

export function inline(text) {
  const out = [];
  let last = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[1] !== undefined) out.push(el('code', {}, [m[1]]));
    else if (m[2] !== undefined) {
      // A non-http(s) target (javascript:, data:, a relative path) stays text.
      out.push(SAFE_URL.test(m[3]) ? link(m[3], inline(m[2])) : m[0]);
    } else if (m[4] !== undefined) out.push(el('strong', {}, inline(m[4])));
    else if (m[5] !== undefined) out.push(el('strong', {}, inline(m[5])));
    else if (m[6] !== undefined) out.push(el('em', {}, inline(m[6])));
    else if (m[7] !== undefined) out.push(el('em', {}, inline(m[7])));
    else if (m[8] !== undefined) {
      // Sentence punctuation after a bare URL is not part of it.
      const url = m[8].replace(/[.,;:!?'"]+$/, '');
      out.push(link(url, [url]));
      if (url.length < m[8].length) out.push(m[8].slice(url.length));
    }
    else out.push(el('a', { href: `#/issue/${m[9]}` }, [`#${m[9]}`]));
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

const BULLET = /^(\s*)([-*+])\s+(.*)$/;
const NUMBER = /^(\s*)(\d+)[.)]\s+(.*)$/;
const TASK = /^\[([ xX])\]\s+(.*)$/;

function listItem(rest) {
  const task = TASK.exec(rest);
  if (!task) return { kids: inline(rest) };
  const box = el('input', { type: 'checkbox', disabled: '' });
  if (task[1] !== ' ') box.setAttribute('checked', '');
  return { kids: [box, ' ', ...inline(task[2])], task: true };
}

// parseList consumes consecutive list lines at or deeper than the first
// line's indent and returns the list element plus the next line index.
function parseList(lines, start) {
  const first = BULLET.exec(lines[start]) || NUMBER.exec(lines[start]);
  const base = first[1].length;
  const ordered = NUMBER.test(lines[start]) && !BULLET.test(lines[start]);
  const list = el(ordered ? 'ol' : 'ul', {}, []);
  let i = start;
  let current = null;
  while (i < lines.length) {
    const m = BULLET.exec(lines[i]) || NUMBER.exec(lines[i]);
    if (!m) break;
    const indent = m[1].length;
    if (indent < base) break;
    if (indent >= base + 2 && current) {
      const [nested, next] = parseList(lines, i);
      current.append(nested);
      i = next;
      continue;
    }
    const item = listItem(m[3]);
    current = el('li', item.task ? { class: 'task' } : {}, item.kids);
    list.append(current);
    i++;
  }
  return [list, i];
}

export function renderMarkdown(source) {
  const root = el('div', { class: 'md' }, []);
  const lines = String(source || '').replace(/\r\n?/g, '\n').split('\n');
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === '') { i++; continue; }

    const fence = /^\s*(```|~~~)/.exec(line);
    if (fence) {
      const body = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence[1])) body.push(lines[i++]);
      i++; // closing fence; a missing one runs to the end of the text
      root.append(el('pre', {}, [el('code', {}, [body.join('\n')])]));
      continue;
    }

    const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (heading) {
      root.append(el('h' + heading[1].length, {}, inline(heading[2])));
      i++;
      continue;
    }

    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      root.append(el('hr', {}, []));
      i++;
      continue;
    }

    if (/^\s*>/.test(line)) {
      const quoted = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) quoted.push(lines[i++].replace(/^\s*>\s?/, ''));
      root.append(el('blockquote', {}, renderMarkdown(quoted.join('\n')).childNodes));
      continue;
    }

    if (BULLET.test(line) || NUMBER.test(line)) {
      const [list, next] = parseList(lines, i);
      root.append(list);
      i = next;
      continue;
    }

    // Paragraph: runs until a blank line or the start of another block.
    const para = [];
    while (i < lines.length && lines[i].trim() !== ''
      && !/^\s*(```|~~~)/.test(lines[i]) && !/^#{1,6}\s/.test(lines[i])
      && !/^\s*>/.test(lines[i]) && !BULLET.test(lines[i]) && !NUMBER.test(lines[i])) {
      para.push(lines[i++]);
    }
    const kids = [];
    para.forEach((l, n) => {
      if (n > 0) kids.push(el('br', {}, []));
      kids.push(...inline(l));
    });
    root.append(el('p', {}, kids));
  }
  return root;
}
