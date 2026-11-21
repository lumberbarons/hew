import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { api, explain } from '../api';
import { useAct } from '../toast';
import {
  IDEA_TYPE,
  ISSUE_TYPES,
  type CheckResult,
  type CreateResult,
  type Finding,
} from '../types';
import { Field, PageHead } from '../ui';

export interface FormState {
  kind: 'issue' | 'epic';
  title: string;
  type: string;
  prio: string;
  areas: string;
  where: string;
  problem: string;
  fix: string;
  questions: string;
  done: string;
  blockedBy: string;
  parent: string;
  children: string;
}

const EMPTY: FormState = {
  kind: 'issue',
  title: '',
  type: 'task',
  prio: 'P2',
  areas: '',
  where: '',
  problem: '',
  fix: '',
  questions: '',
  done: '',
  blockedBy: '',
  parent: '',
  children: '',
};

const ints = (s: string): number[] =>
  s
    .split(/[\s,]+/)
    .map((x) => Number.parseInt(x.replace('#', ''), 10))
    .filter(Number.isInteger);

const lines = (s: string): string[] =>
  s
    .split('\n')
    .map((x) => x.trim())
    .filter(Boolean);

/**
 * The request a form state becomes. A bug's sections are Problem and Fix; a
 * task, enhancement or epic uses Goal and Approach, so one pair of boxes
 * serves both and only the wire names change. An idea is Problem plus its open
 * questions, with no approach or checklist: it is captured to decide whether
 * to investigate, not to describe the work.
 */
export function buildPayload(f: FormState): { path: string; body: Record<string, unknown> } {
  const idea = f.kind === 'issue' && f.type === IDEA_TYPE;
  const bug = f.kind === 'issue' && f.type === 'bug';
  const questions = lines(f.questions);
  const sections: Record<string, unknown> = idea
    ? { problem: f.problem, ...(questions.length > 0 ? { openQuestions: questions } : {}) }
    : {
        where: f.where,
        doneWhen: lines(f.done),
        ...(bug ? { problem: f.problem, fix: f.fix } : { goal: f.problem, approach: f.fix }),
      };
  if (f.kind === 'epic') {
    return {
      path: '/api/epics',
      body: { title: f.title, children: ints(f.children), body: sections },
    };
  }
  return {
    path: '/api/issues',
    body: {
      title: f.title,
      type: f.type,
      priority: f.prio,
      areas: f.areas
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean),
      // The Parent field is hidden for an idea, which stays independent of any
      // epic, so a value typed before switching the type must not ride along.
      parent: idea ? 0 : (ints(f.parent)[0] ?? 0),
      blockedBy: ints(f.blockedBy),
      body: sections,
    },
  };
}

type Status = { ok: true; findings: Finding[] } | { ok: false; message: string } | null;

export function NewForm({ type }: { type?: string }) {
  const act = useAct();
  const [form, setForm] = useState<FormState>(() => ({
    ...EMPTY,
    type: type && (ISSUE_TYPES as readonly string[]).includes(type) ? type : EMPTY.type,
  }));
  const [touched, setTouched] = useState(false);
  const [status, setStatus] = useState<Status>(null);
  const [submitting, setSubmitting] = useState(false);
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    setTouched(true);
    setForm((f) => ({ ...f, [key]: value }));
  };

  const req = useMemo(() => buildPayload(form), [form]);
  const epic = form.kind === 'epic';
  const idea = !epic && form.type === IDEA_TYPE;
  const bug = !epic && form.type === 'bug';

  // Validate through the write path's own dry run, so the form never offers
  // what the library would refuse. Nothing runs until the person has typed.
  useEffect(() => {
    if (!touched) return;
    let live = true;
    const timer = setTimeout(() => {
      api<CheckResult>('POST', `${req.path}?dryRun=1`, req.body).then(
        (r) => live && setStatus({ ok: true, findings: r.findings }),
        (e: unknown) => live && setStatus({ ok: false, message: explain(e) }),
      );
    }, 300);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [touched, req]);

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    setSubmitting(true);
    const r = await act(() => api<CreateResult>('POST', req.path, req.body), 'Created');
    setSubmitting(false);
    if (r) window.location.hash = `#/issue/${r.issue.number}`;
  };

  return (
    <>
      <PageHead title="New" sub="File an issue or an epic" />
      <form className="card form" onSubmit={(ev) => void submit(ev)}>
        <div className="form-section">
          <h3>Basics</h3>
          <div className={epic ? 'grid-2' : 'grid-3'}>
            <Field label="Kind">
              <select
                value={form.kind}
                onChange={(e) => set('kind', e.target.value as FormState['kind'])}
              >
                <option value="issue">Issue</option>
                <option value="epic">Epic</option>
              </select>
            </Field>
            <Field label="Type" hidden={epic}>
              <select value={form.type} onChange={(e) => set('type', e.target.value)}>
                {ISSUE_TYPES.map((t) => (
                  <option key={t}>{t}</option>
                ))}
              </select>
            </Field>
            <Field label="Priority" hidden={epic}>
              <select value={form.prio} onChange={(e) => set('prio', e.target.value)}>
                {['P2', 'P0', 'P1', 'P3', 'P4'].map((p) => (
                  <option key={p}>{p}</option>
                ))}
              </select>
            </Field>
          </div>
          <Field label="Title">
            <input
              type="text"
              autoComplete="off"
              value={form.title}
              onChange={(e) => set('title', e.target.value)}
            />
          </Field>
          <Field
            label="Areas"
            hidden={epic}
            hint="Comma separated. Use sparingly: only once several issues share one."
          >
            <input
              type="text"
              placeholder="e.g. cli, docs"
              value={form.areas}
              onChange={(e) => set('areas', e.target.value)}
            />
          </Field>
        </div>

        <div className="form-section">
          <h3>
            Description
            <small>Section headings are added for you. Leave out any that do not apply.</small>
          </h3>
          <Field label="Where" hidden={idea} hint="Files, packages or areas involved.">
            <textarea rows={2} value={form.where} onChange={(e) => set('where', e.target.value)} />
          </Field>
          <Field label={bug ? 'Problem' : idea ? 'Problem or opportunity' : 'Goal'}>
            <textarea
              rows={4}
              value={form.problem}
              onChange={(e) => set('problem', e.target.value)}
            />
          </Field>
          <Field label={bug ? 'Fix' : 'Approach'} hidden={idea}>
            <textarea rows={4} value={form.fix} onChange={(e) => set('fix', e.target.value)} />
          </Field>
          <Field
            label="Open questions"
            hidden={!idea}
            hint="One per line. Optional: add them when you know what needs investigating."
          >
            <textarea
              rows={3}
              value={form.questions}
              onChange={(e) => set('questions', e.target.value)}
            />
          </Field>
          <Field label="Done when" hidden={idea} hint="One checklist item per line.">
            <textarea rows={4} value={form.done} onChange={(e) => set('done', e.target.value)} />
          </Field>
        </div>

        <div className="form-section">
          <h3>Relationships</h3>
          {!epic && (
            <div className="grid-2">
              <Field label="Blocked by" hint="Issues that must close first.">
                <input
                  type="text"
                  placeholder="#3, #4"
                  value={form.blockedBy}
                  onChange={(e) => set('blockedBy', e.target.value)}
                />
              </Field>
              <Field label="Parent epic" hidden={idea}>
                <input
                  type="text"
                  placeholder="#12"
                  value={form.parent}
                  onChange={(e) => set('parent', e.target.value)}
                />
              </Field>
            </div>
          )}
          {epic && (
            <Field
              label="Children"
              hint="Existing issues to attach as sub-issues. They must not already have a parent."
            >
              <input
                type="text"
                placeholder="#3, #4"
                value={form.children}
                onChange={(e) => set('children', e.target.value)}
              />
            </Field>
          )}
        </div>

        <div className="form-foot">
          <div className="status" role="status">
            {status?.ok === true && (
              <>
                <div className="note ok">Ready to create.</div>
                {status.findings.map((f, idx) => (
                  <div key={idx} className="note warn">
                    Wrap in backticks: {f.tokens.join(', ')}
                  </div>
                ))}
              </>
            )}
            {status?.ok === false && <div className="note bad">{status.message}</div>}
          </div>
          <button
            className="ghost"
            type="button"
            onClick={() => {
              window.location.hash = '#/';
            }}
          >
            Cancel
          </button>
          <button className="primary" type="submit" disabled={submitting}>
            Create
          </button>
        </div>
      </form>
    </>
  );
}
