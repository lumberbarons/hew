import { useState, type ReactNode } from 'react';
import { Await } from '../Await';
import { Markdown } from '../Markdown';
import { api } from '../api';
import { useAct, useToast } from '../toast';
import type { Detail } from '../types';
import { Chip, Field, num, prioClass, Section } from '../ui';
import { useLoad } from '../useLoad';

const PRIORITIES = ['P0', 'P1', 'P2', 'P3', 'P4'];
const TYPES = ['bug', 'enhancement', 'task'];

export function IssueView({ n }: { n: number }) {
  const state = useLoad(() => api<Detail>('GET', `/api/issues/${n}`));
  return <Await state={state}>{(d, reload) => <IssueBody d={d} reload={reload} />}</Await>;
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="prow">
      <div className="label">{label}</div>
      {children}
    </div>
  );
}

function IssueBody({ d, reload }: { d: Detail; reload: () => void }) {
  const i = d.issue;
  const n = i.number;
  const open = i.state === 'open';
  const claimed = i.inProgress || i.assignees.length > 0;
  const findings = d.textFindings.map((f, idx) => (
    <div key={idx} className="note warn">
      Unusual characters in {f.source}:{' '}
      {(['zeroWidth', 'bidiControl', 'unicodeTags', 'confusable'] as const)
        .filter((k) => f[k])
        .join(', ')}
    </div>
  ));

  return (
    <>
      <div className="issue-head">
        <h1>
          <span className="n">{num(n)}</span>
          {i.title}
        </h1>
      </div>
      <div className="issue-meta">
        <Chip cls={prioClass(i.priority)}>{i.priority ?? 'P?'}</Chip>
        {i.type && <Chip>{i.type}</Chip>}
        {i.epic && <Chip>epic</Chip>}
        <Chip cls={open ? '' : 'state-closed'}>{i.state}</Chip>
        {i.areas.map((a) => (
          <Chip key={a}>{a}</Chip>
        ))}
        {claimed && <Chip cls="claim">in progress @{i.assignees.join(', @')}</Chip>}
        {i.parent && (
          <span className="muted">
            in epic <a href={`#/issue/${i.parent}`}>{num(i.parent)}</a>
          </span>
        )}
      </div>
      {findings}
      <div className="card prows">
        {open && <Work d={d} reload={reload} claimed={claimed} />}
        <Closer key={open ? 'close' : 'reopen'} d={d} reload={reload} open={open} />
        <Dependencies d={d} reload={reload} />
      </div>
      <EditPanel key={`${i.title}|${i.priority}|${i.type}`} d={d} reload={reload} open={open} />
      <h2>Body</h2>
      {i.body ? <Markdown>{i.body}</Markdown> : <p className="muted">(empty)</p>}
      {d.children.length > 0 && (
        <Section
          title="Children"
          issues={d.children}
          count={`${d.children.length}${d.next ? ` · next ${num(d.next)}` : ''}`}
        />
      )}
      {i.comments && i.comments.length > 0 && (
        <>
          <h2>
            Comments<span className="count"> · {i.commentsTotal ?? i.comments.length}</span>
          </h2>
          {i.comments.map((c, idx) => (
            <div key={idx} className="card comment">
              <div className="who">
                {c.author} · {new Date(c.createdAt).toLocaleString()}
              </div>
              <Markdown>{c.body}</Markdown>
            </div>
          ))}
        </>
      )}
    </>
  );
}

function Work({ d, reload, claimed }: { d: Detail; reload: () => void; claimed: boolean }) {
  const act = useAct();
  const i = d.issue;
  const [priority, setPriority] = useState('');
  const needsPriority = i.untriaged || !i.priority;

  if (i.epic) {
    return (
      <Row label="Work">
        <div className="toolbar">
          <span className="muted">Epics are containers. Work their children instead.</span>
        </div>
      </Row>
    );
  }
  return (
    <Row label="Work">
      <div className="toolbar">
        {needsPriority && (
          <select
            aria-label="Priority"
            style={{ width: 'auto' }}
            value={priority}
            onChange={(e) => setPriority(e.target.value)}
          >
            <option value="">{i.untriaged ? 'Choose priority…' : 'Keep priority'}</option>
            {PRIORITIES.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        )}
        <button
          className="primary"
          onClick={async () => {
            if (
              await act(
                () => api('POST', `/api/issues/${i.number}/start`, { priority }),
                `Started ${num(i.number)}`,
              )
            )
              reload();
          }}
        >
          Start
        </button>
        {claimed && (
          <button
            onClick={async () => {
              if (
                await act(
                  () => api('POST', `/api/issues/${i.number}/release`, {}),
                  `Released ${num(i.number)}`,
                )
              )
                reload();
            }}
          >
            Release
          </button>
        )}
      </div>
    </Row>
  );
}

function Closer({ d, reload, open }: { d: Detail; reload: () => void; open: boolean }) {
  const act = useAct();
  const n = d.issue.number;
  const [reason, setReason] = useState('');
  const [completed, setCompleted] = useState(false);

  const submit = async () => {
    const call = open
      ? () => api('POST', `/api/issues/${n}/close`, { reason, completed })
      : () => api('POST', `/api/issues/${n}/reopen`, { reason });
    if (await act(call, open ? `Closed ${num(n)}` : `Reopened ${num(n)}`)) reload();
  };

  return (
    <Row label={open ? 'Close' : 'Reopen'}>
      <div className="toolbar">
        <input
          type="text"
          aria-label="Reason"
          placeholder={open ? 'Reason for closing (required)' : 'Reason for reopening (required)'}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        {open && (
          <label className="check">
            <input
              type="checkbox"
              checked={completed}
              onChange={(e) => setCompleted(e.target.checked)}
            />
            Completed
          </label>
        )}
        <button className={open ? 'danger' : ''} onClick={() => void submit()}>
          {open ? 'Close issue' : 'Reopen issue'}
        </button>
      </div>
    </Row>
  );
}

function Dependencies({ d, reload }: { d: Detail; reload: () => void }) {
  const act = useAct();
  const toast = useToast();
  const n = d.issue.number;
  const [blocker, setBlocker] = useState('');

  const add = async () => {
    const m = Number.parseInt(blocker.replace('#', ''), 10);
    if (!Number.isInteger(m)) {
      toast('Enter the number of the issue that blocks this one.', 'bad');
      return;
    }
    const added = await act(
      () => api('POST', `/api/issues/${n}/blockers`, { blockers: [m] }),
      `Blocked on ${num(m)}`,
    );
    if (added) {
      setBlocker('');
      reload();
    }
  };

  return (
    <Row label="Blocked by">
      <div className="toolbar">
        {d.issue.blockedBy.length === 0 && <span className="muted">Nothing</span>}
        {d.issue.blockedBy.map((b) => (
          <Chip key={b} cls="block">
            <a href={`#/issue/${b}`}>{num(b)}</a>
            <a
              href="#"
              title="Remove"
              aria-label={`Remove blocker ${num(b)}`}
              onClick={async (ev) => {
                ev.preventDefault();
                if (
                  await act(
                    () => api('DELETE', `/api/issues/${n}/blockers/${b}?allowClosed=1`),
                    `Unblocked from ${num(b)}`,
                  )
                )
                  reload();
              }}
            >
              ×
            </a>
          </Chip>
        ))}
        <input
          type="text"
          aria-label="Blocked by issue number"
          placeholder="Blocked by #…"
          style={{ maxWidth: 200 }}
          value={blocker}
          onChange={(e) => setBlocker(e.target.value)}
        />
        <button onClick={() => void add()}>Add blocker</button>
      </div>
    </Row>
  );
}

function EditPanel({ d, reload, open }: { d: Detail; reload: () => void; open: boolean }) {
  const act = useAct();
  const i = d.issue;
  const [title, setTitle] = useState(i.title);
  const [priority, setPriority] = useState(i.priority ?? 'P2');
  const [type, setType] = useState(i.type ?? 'task');
  const [area, setArea] = useState('');

  const save = async () => {
    const req: Record<string, unknown> = { allowClosed: !open };
    if (title !== i.title) req.title = title;
    if (priority !== i.priority) req.priority = priority;
    if (type !== i.type) req.type = type;
    if (area.trim()) req.addAreas = [area.trim()];
    if (await act(() => api('PATCH', `/api/issues/${i.number}`, req), 'Saved')) reload();
  };

  return (
    <details className="card panel">
      <summary>Edit details</summary>
      <Field label="Title">
        <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} />
      </Field>
      <div className="grid-3">
        <Field label="Priority">
          <select value={priority} onChange={(e) => setPriority(e.target.value)}>
            {PRIORITIES.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </Field>
        <Field label="Type">
          <select value={type} onChange={(e) => setType(e.target.value)}>
            {TYPES.map((t) => (
              <option key={t}>{t}</option>
            ))}
          </select>
        </Field>
        <Field label="Add area">
          <input
            type="text"
            placeholder="e.g. cli"
            value={area}
            onChange={(e) => setArea(e.target.value)}
          />
        </Field>
      </div>
      <div className="toolbar">
        <button className="primary" onClick={() => void save()}>
          Save changes
        </button>
      </div>
    </details>
  );
}
