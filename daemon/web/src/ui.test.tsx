import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { fx } from './test/fixtures';
import { Chip, Field, IssueRow, PageHead, Section, Warnings, num, prioClass } from './ui';
import type { Issue } from './types';

const issue = (over: Partial<Issue> = {}): Issue => ({ ...fx.board.ready[0]!, ...over });

describe('helpers', () => {
  it('formats numbers and priority classes', () => {
    expect(num(7)).toBe('#7');
    expect(prioClass('P1')).toBe('p1');
    expect(prioClass('P4')).toBe('p4');
    expect(prioClass(null)).toBe('p');
  });
});

describe('IssueRow', () => {
  it('links the number and title and shows priority, type and areas', () => {
    render(
      <IssueRow
        issue={issue({ number: 7, title: 'Do it', priority: 'P1', type: 'task', areas: ['cli'] })}
      />,
    );
    expect(screen.getByRole('link', { name: '#7' })).toHaveAttribute('href', '#/issue/7');
    expect(screen.getByRole('link', { name: 'Do it' })).toHaveAttribute('href', '#/issue/7');
    expect(screen.getByText('P1')).toHaveClass('p1');
    expect(screen.getByText('task')).toBeInTheDocument();
    expect(screen.getByText('cli')).toBeInTheDocument();
  });

  it('marks an untriaged priority as unknown', () => {
    render(<IssueRow issue={issue({ priority: null, type: null })} />);
    expect(screen.getByText('P?')).toBeInTheDocument();
  });

  it('shows who has claimed it, or just that it is in progress', () => {
    const { rerender } = render(
      <IssueRow issue={issue({ inProgress: true, assignees: ['alice', 'bob'] })} />,
    );
    expect(screen.getByText('in progress @alice, @bob')).toHaveClass('claim');
    rerender(<IssueRow issue={issue({ inProgress: true, assignees: [] })} />);
    expect(screen.getByText('in progress')).toBeInTheDocument();
  });

  it('shows what blocks it', () => {
    render(<IssueRow issue={issue({ openBlockers: [1, 2] })} />);
    expect(screen.getByText('blocked by #1, #2')).toHaveClass('block');
  });

  it('shows an epic with its progress', () => {
    const { container } = render(
      <IssueRow issue={issue({ epic: true, subIssuesTotal: 4, subIssuesCompleted: 1 })} />,
    );
    expect(screen.getByText('epic')).toBeInTheDocument();
    expect(screen.getByText('1/4')).toBeInTheDocument();
    expect(container.querySelector('.bar i')).toHaveStyle({ width: '25%' });
  });

  it('shows no progress for an epic with no children yet', () => {
    const { container } = render(<IssueRow issue={issue({ epic: true, subIssuesTotal: 0 })} />);
    expect(container.querySelector('.bar')).toBeNull();
  });

  it('renders no meta block for a plain issue', () => {
    const { container } = render(<IssueRow issue={issue({ type: null, areas: [] })} />);
    expect(container.querySelector('.meta')).toBeNull();
  });
});

describe('Section', () => {
  it('lists issues under a counted heading', () => {
    render(<Section title="Ready" issues={fx.board.ready} />);
    expect(screen.getByRole('heading', { name: /Ready/ })).toHaveTextContent(
      `· ${fx.board.ready.length}`,
    );
  });

  it('says so when it is empty, with its own wording if given', () => {
    const { rerender } = render(<Section title="Blocked" issues={[]} />);
    expect(screen.getByText('Nothing here.')).toBeInTheDocument();
    rerender(<Section title="Ready" issues={[]} empty="No ready work." />);
    expect(screen.getByText('No ready work.')).toBeInTheDocument();
  });

  it('shows a custom count', () => {
    render(<Section title="Children" issues={fx.board.ready} count="2 · next #4" />);
    expect(screen.getByRole('heading')).toHaveTextContent('· 2 · next #4');
  });
});

describe('Warnings', () => {
  it('spells out a dependency cycle', () => {
    render(
      <Warnings
        warnings={[{ kind: 'dependency-cycle', issue: 0, cycle: [7, 8, 7], total: 0, fetched: 0 }]}
      />,
    );
    expect(screen.getByText(/#7 → #8 → #7/)).toBeInTheDocument();
  });

  it('describes a capped connection', () => {
    render(
      <Warnings
        warnings={[{ kind: 'sub-issues-truncated', issue: 4, cycle: [], total: 120, fetched: 100 }]}
      />,
    );
    expect(screen.getByText('sub-issues-truncated on #4 (100 of 120 fetched)')).toBeInTheDocument();
  });

  it('describes a warning with no subject', () => {
    render(<Warnings warnings={[{ kind: 'odd', issue: 0, cycle: [], total: 0, fetched: 0 }]} />);
    expect(screen.getByText('odd')).toBeInTheDocument();
  });
});

describe('Field', () => {
  it('ties the label to its control', () => {
    render(
      <Field label="Title" hint="Short and specific.">
        <input type="text" />
      </Field>,
    );
    expect(screen.getByLabelText('Title')).toBeInTheDocument();
    expect(screen.getByText('Short and specific.')).toBeInTheDocument();
  });

  it('can be hidden', () => {
    render(
      <Field label="Type" hidden>
        <input type="text" />
      </Field>,
    );
    expect(
      screen.getByLabelText('Type', { selector: 'input' }).closest('.field'),
    ).not.toBeVisible();
  });
});

describe('small pieces', () => {
  it('renders chips and page heads', () => {
    render(
      <>
        <Chip cls="claim">mine</Chip>
        <PageHead title="Board" sub="all of it" />
      </>,
    );
    expect(screen.getByText('mine')).toHaveClass('chip', 'claim');
    expect(screen.getByRole('heading', { name: 'Board' })).toBeInTheDocument();
    expect(screen.getByText('all of it')).toBeInTheDocument();
  });
});
