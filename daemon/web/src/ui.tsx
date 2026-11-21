import { cloneElement, useId, type ReactElement, type ReactNode } from 'react';
import { IDEA_TYPE, type Issue, type TextFinding, type Warning } from './types';

export const num = (n: number) => `#${n}`;

/** "P1" becomes "p1"; a missing priority is "P?" and gets the neutral class. */
export const prioClass = (p: string | null) => `p${(p ?? '?').slice(1).replace('?', '')}`;

export function Chip({ cls, children }: { cls?: string; children: ReactNode }) {
  return <span className={cls ? `chip ${cls}` : 'chip'}>{children}</span>;
}

export function ClaimChip({ issue }: { issue: Issue }) {
  const owners = issue.assignees.map((name) => `@${name}`).join(', ');
  if (issue.type === IDEA_TYPE && !issue.epic) {
    if (issue.state === 'closed') {
      return owners ? <Chip>Last assigned to {owners}</Chip> : null;
    }
    if (!issue.inProgress && !owners) return null;
    return <Chip cls="claim">Investigating{owners ? ` · ${owners}` : ''}</Chip>;
  }
  if (!issue.inProgress && !owners) return null;
  return <Chip cls="claim">in progress{owners ? ` ${owners}` : ''}</Chip>;
}

export function IssueRow({ issue: i }: { issue: Issue }) {
  const pct = i.subIssuesTotal ? Math.round((100 * i.subIssuesCompleted) / i.subIssuesTotal) : 0;
  const meta: ReactNode[] = [];
  if (i.epic) meta.push(<Chip key="epic">epic</Chip>);
  if (i.type) meta.push(<Chip key="type">{i.type}</Chip>);
  i.areas.forEach((a) => meta.push(<Chip key={`area-${a}`}>{a}</Chip>));
  if (i.openBlockers.length > 0) {
    meta.push(
      <Chip key="blocked" cls="block">
        blocked by {i.openBlockers.map(num).join(', ')}
      </Chip>,
    );
  }
  if (i.epic && i.subIssuesTotal > 0) {
    meta.push(
      <span key="progress" className="progress">
        {i.subIssuesCompleted}/{i.subIssuesTotal}
        <span className="bar">
          <i style={{ width: `${pct}%` }} />
        </span>
      </span>,
    );
  }
  if (i.inProgress || i.assignees.length > 0) {
    meta.push(<ClaimChip key="claim" issue={i} />);
  }
  return (
    <div className="row">
      <a className="num" href={`#/issue/${i.number}`}>
        {num(i.number)}
      </a>
      <Chip cls={prioClass(i.priority)}>{i.priority ?? 'P?'}</Chip>
      <a className="title" href={`#/issue/${i.number}`}>
        {i.title}
      </a>
      {meta.length > 0 && <div className="meta">{meta}</div>}
    </div>
  );
}

export function Section({
  title,
  issues,
  empty = 'Nothing here.',
  count,
}: {
  title: string;
  issues: Issue[];
  empty?: string;
  count?: ReactNode;
}) {
  return (
    <>
      <h2>
        {title}
        <span className="count"> · {count ?? issues.length}</span>
      </h2>
      <div className="list">
        {issues.length > 0 ? (
          issues.map((i) => <IssueRow key={i.number} issue={i} />)
        ) : (
          <div className="empty">{empty}</div>
        )}
      </div>
    </>
  );
}

export function Warnings({ warnings }: { warnings: Warning[] }) {
  return (
    <>
      {warnings.map((w, idx) => (
        <div key={idx} className="note warn">
          {w.kind === 'dependency-cycle'
            ? `Dependency cycle (members hidden from ready): ${w.cycle.map(num).join(' → ')}`
            : `${w.kind}${w.issue ? ` on ${num(w.issue)}` : ''}${w.total ? ` (${w.fetched} of ${w.total} fetched)` : ''}`}
        </div>
      ))}
    </>
  );
}

/** A labelled control; the label is tied to it by id so clicking focuses it. */
export function Field({
  label,
  hint,
  hidden,
  children,
}: {
  label: string;
  hint?: string;
  hidden?: boolean;
  children: ReactElement<{ id?: string }>;
}) {
  const id = useId();
  return (
    <div className="field" hidden={hidden}>
      <label htmlFor={id}>{label}</label>
      {cloneElement(children, { id })}
      {hint && <p className="hint">{hint}</p>}
    </div>
  );
}

export function PageHead({ title, sub }: { title: string; sub?: string }) {
  return (
    <div className="page-head">
      <h1>{title}</h1>
      {sub && <span className="sub">{sub}</span>}
    </div>
  );
}

const FINDING_KINDS = ['zeroWidth', 'bidiControl', 'unicodeTags', 'confusable'] as const;

/**
 * Notes about invisible or look-alike characters in issue text, which can hide
 * instructions from the person reading it. withIssue names the issue, for a
 * list where the finding is not on the page it describes.
 */
export function TextFindings({
  findings,
  withIssue,
}: {
  findings: TextFinding[];
  withIssue?: boolean;
}) {
  return (
    <>
      {findings.map((f, idx) => (
        <div key={idx} className="note warn">
          {withIssue && `${num(f.issue)}: `}Unusual characters in {f.source}:{' '}
          {FINDING_KINDS.filter((k) => f[k]).join(', ')}
        </div>
      ))}
    </>
  );
}
