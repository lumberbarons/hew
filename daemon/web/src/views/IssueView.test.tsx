import { screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { Detail } from '../types';
import { fx } from '../test/fixtures';
import { mockApi, status } from '../test/mockApi';
import { renderView } from '../test/render';
import { IssueView } from './IssueView';

const withIssue = (d: Detail, over: Partial<Detail['issue']>): Detail => ({
  ...d,
  issue: { ...d.issue, ...over },
});
const reads = (d: Detail) => ({ [`GET /api/issues/${d.issue.number}`]: d });

describe('IssueView', () => {
  it('shows an issue with its state, claim and body', async () => {
    mockApi(reads(fx.claimed));
    renderView(<IssueView n={2} />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
      '#2Crash on empty input',
    );
    const meta = within(document.querySelector('.issue-meta') as HTMLElement);
    expect(meta.getByText('P0')).toHaveClass('p0');
    expect(meta.getByText('bug')).toBeInTheDocument();
    expect(meta.getByText('in progress @alice')).toHaveClass('claim');
    expect(screen.getByText('Guard the slice')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Release' })).toBeInTheDocument();
  });

  it('reports an issue that does not exist', async () => {
    mockApi({
      'GET /api/issues/9': status(404, {
        error: 'not_found',
        message: 'issue #9 not found',
        fields: { issue: 9 },
      }),
    });
    renderView(<IssueView n={9} />);
    expect(await screen.findByText('issue #9 not found')).toBeInTheDocument();
  });

  it('links an issue to its epic and says when the body is empty', async () => {
    mockApi(reads(withIssue(fx.blocked, { body: '' })));
    renderView(<IssueView n={3} />);
    expect(await screen.findByRole('link', { name: '#4' })).toHaveAttribute('href', '#/issue/4');
    expect(screen.getByText('(empty)')).toBeInTheDocument();
  });

  it('warns about unusual characters in the text', async () => {
    mockApi(reads(fx.odd));
    renderView(<IssueView n={6} />);
    expect(await screen.findByText(/Unusual characters in title: zeroWidth/)).toBeInTheDocument();
    // The body is rendered as untrusted text and the #1 reference is a link.
    expect(screen.getByRole('link', { name: '#1' })).toHaveAttribute('href', '#/issue/1');
    expect(document.querySelector('script')).toBeNull();
  });

  describe('claiming', () => {
    it('explains a claim someone else holds, as the CLI would', async () => {
      mockApi({ ...reads(fx.claimed), 'POST /api/issues/2/start': status(409, fx.claimedByOther) });
      const { user } = renderView(<IssueView n={2} />);
      await user.click(await screen.findByRole('button', { name: 'Start' }));
      expect(
        await screen.findByText('#2 is claimed by alice. Pick the next ready item.'),
      ).toBeInTheDocument();
    });

    it('explains an issue with open blockers', async () => {
      mockApi({ ...reads(fx.blocked), 'POST /api/issues/3/start': status(409, fx.blockedError) });
      const { user } = renderView(<IssueView n={3} />);
      await user.click(await screen.findByRole('button', { name: 'Start' }));
      expect(
        await screen.findByText('#3 has open blockers: #1. Pick another item.'),
      ).toBeInTheDocument();
    });

    it('refetches after a successful start', async () => {
      const { calls } = mockApi({
        ...reads(fx.blocked),
        'POST /api/issues/3/start': { issue: {}, claimant: 'daemon' },
      });
      const { user } = renderView(<IssueView n={3} />);
      await user.click(await screen.findByRole('button', { name: 'Start' }));
      expect(await screen.findByText('Started #3')).toBeInTheDocument();
      await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2));
      expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ priority: '' });
    });

    it('asks for a priority when claiming untriaged work', async () => {
      const d = withIssue(fx.blocked, { untriaged: true, priority: null, type: null });
      const { calls } = mockApi({ ...reads(d), 'POST /api/issues/3/start': {} });
      const { user } = renderView(<IssueView n={3} />);
      const choose = await screen.findByLabelText('Priority', { selector: 'select[aria-label]' });
      expect(within(choose).getByRole('option', { name: 'Choose priority…' })).toBeInTheDocument();
      await user.selectOptions(choose, 'P1');
      await user.click(screen.getByRole('button', { name: 'Start' }));
      await waitFor(() =>
        expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ priority: 'P1' }),
      );
    });

    it('offers to keep the priority when an issue has none but is triaged elsewhere', async () => {
      mockApi(reads(withIssue(fx.blocked, { priority: null })));
      renderView(<IssueView n={3} />);
      const choose = await screen.findByLabelText('Priority', { selector: 'select[aria-label]' });
      expect(within(choose).getByRole('option', { name: 'Keep priority' })).toBeInTheDocument();
    });

    it('releases a claim', async () => {
      const { calls } = mockApi({
        ...reads(fx.claimed),
        'POST /api/issues/2/release': { released: true },
      });
      const { user } = renderView(<IssueView n={2} />);
      await user.click(await screen.findByRole('button', { name: 'Release' }));
      expect(await screen.findByText('Released #2')).toBeInTheDocument();
      expect(calls.some((c) => c.path === '/api/issues/2/release')).toBe(true);
    });

    it('does not offer to work an epic, and lists its children', async () => {
      mockApi({ ...reads(fx.epic), 'GET /api/issues/1': fx.claimed });
      renderView(<IssueView n={4} />);
      expect(await screen.findByText(/Epics are containers/)).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Start' })).toBeNull();
      expect(screen.getByRole('heading', { name: /Children/ })).toHaveTextContent('2 · next #1');
      expect(screen.getByRole('link', { name: 'Wire up the importer' })).toBeInTheDocument();
    });

    it('lists children without a next when the sequence is dry', async () => {
      mockApi(reads({ ...fx.epic, next: null }));
      renderView(<IssueView n={4} />);
      expect(await screen.findByRole('heading', { name: /Children/ })).toHaveTextContent('· 2');
      expect(screen.getByRole('heading', { name: /Children/ })).not.toHaveTextContent('next');
    });
  });

  describe('closing', () => {
    it('closes with a reason and whether the work was completed', async () => {
      const { calls } = mockApi({
        ...reads(fx.claimed),
        'POST /api/issues/2/close': { stateReason: 'completed' },
      });
      const { user } = renderView(<IssueView n={2} />);
      await user.type(await screen.findByLabelText('Reason'), 'fixed upstream');
      await user.click(screen.getByLabelText('Completed'));
      await user.click(screen.getByRole('button', { name: 'Close issue' }));
      expect(await screen.findByText('Closed #2')).toBeInTheDocument();
      expect(calls.find((c) => c.path.endsWith('/close'))?.body).toEqual({
        reason: 'fixed upstream',
        completed: true,
      });
    });

    it('explains a close with no reason', async () => {
      mockApi({ ...reads(fx.claimed), 'POST /api/issues/2/close': status(400, fx.invalid) });
      const { user } = renderView(<IssueView n={2} />);
      await user.click(await screen.findByRole('button', { name: 'Close issue' }));
      expect(await screen.findByText('reason: is required')).toBeInTheDocument();
    });

    it('reopens a closed issue and offers no work on it', async () => {
      const closed = withIssue(fx.claimed, { state: 'closed', inProgress: false, assignees: [] });
      const { calls } = mockApi({
        ...reads(closed),
        'POST /api/issues/2/reopen': { reopened: true },
      });
      const { user } = renderView(<IssueView n={2} />);
      expect(await screen.findByText('closed')).toHaveClass('state-closed');
      expect(screen.queryByRole('button', { name: 'Start' })).toBeNull();
      expect(screen.queryByLabelText('Completed')).toBeNull();
      await user.type(screen.getByLabelText('Reason'), 'regressed');
      await user.click(screen.getByRole('button', { name: 'Reopen issue' }));
      expect(await screen.findByText('Reopened #2')).toBeInTheDocument();
      expect(calls.find((c) => c.path.endsWith('/reopen'))?.body).toEqual({ reason: 'regressed' });
    });
  });

  describe('dependencies', () => {
    it('shows what blocks it and removes a blocker', async () => {
      const { calls } = mockApi({
        ...reads(fx.blocked),
        'DELETE /api/issues/3/blockers/1?allowClosed=1': { removed: [1], missing: [] },
      });
      const { user } = renderView(<IssueView n={3} />);
      expect(await screen.findByRole('link', { name: '#1' })).toHaveAttribute('href', '#/issue/1');
      await user.click(screen.getByRole('link', { name: 'Remove blocker #1' }));
      expect(await screen.findByText('Unblocked from #1')).toBeInTheDocument();
      expect(calls.some((c) => c.method === 'DELETE')).toBe(true);
    });

    it('says when nothing blocks it', async () => {
      mockApi(reads(fx.claimed));
      renderView(<IssueView n={2} />);
      expect(await screen.findByText('Nothing')).toBeInTheDocument();
    });

    it('adds a blocker', async () => {
      const { calls } = mockApi({
        ...reads(fx.claimed),
        'POST /api/issues/2/blockers': { added: [1], existing: [] },
      });
      const { user } = renderView(<IssueView n={2} />);
      await user.type(await screen.findByLabelText('Blocked by issue number'), '#1');
      await user.click(screen.getByRole('button', { name: 'Add blocker' }));
      expect(await screen.findByText('Blocked on #1')).toBeInTheDocument();
      expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ blockers: [1] });
    });

    it('does not call the daemon for something that is not an issue number', async () => {
      const { calls } = mockApi(reads(fx.claimed));
      const { user } = renderView(<IssueView n={2} />);
      await user.type(await screen.findByLabelText('Blocked by issue number'), 'soon');
      await user.click(screen.getByRole('button', { name: 'Add blocker' }));
      expect(await screen.findByText(/Enter the number of the issue/)).toBeInTheDocument();
      expect(calls).toHaveLength(1);
    });

    it('explains a dependency cycle', async () => {
      mockApi({
        ...reads(fx.claimed),
        'POST /api/issues/2/blockers': status(409, {
          error: 'cycle',
          message: 'x',
          fields: { cycle: [2, 1, 2] },
        }),
      });
      const { user } = renderView(<IssueView n={2} />);
      await user.type(await screen.findByLabelText('Blocked by issue number'), '1');
      await user.click(screen.getByRole('button', { name: 'Add blocker' }));
      expect(await screen.findByText(/#2 → #1 → #2/)).toBeInTheDocument();
    });
  });

  describe('editing', () => {
    it('sends only what changed', async () => {
      const { calls } = mockApi({
        ...reads(fx.claimed),
        'PATCH /api/issues/2': { applied: ['title'], parentNoop: false },
      });
      const { user } = renderView(<IssueView n={2} />);
      await user.click(await screen.findByText('Edit details'));
      const title = screen.getByLabelText('Title');
      await user.clear(title);
      await user.type(title, 'Crash on empty file');
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      expect(await screen.findByText('Saved')).toBeInTheDocument();
      expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({
        allowClosed: false,
        title: 'Crash on empty file',
      });
    });

    it('changes priority, type and areas', async () => {
      const { calls } = mockApi({
        ...reads(fx.claimed),
        'PATCH /api/issues/2': { applied: [], parentNoop: false },
      });
      const { user } = renderView(<IssueView n={2} />);
      await user.click(await screen.findByText('Edit details'));
      await user.selectOptions(
        screen.getByLabelText('Priority', { selector: 'details select' }),
        'P3',
      );
      await user.selectOptions(screen.getByLabelText('Type'), 'task');
      await user.type(screen.getByLabelText('Add area'), ' docs ');
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() =>
        expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({
          allowClosed: false,
          priority: 'P3',
          type: 'task',
          addAreas: ['docs'],
        }),
      );
    });

    it('allows editing a closed issue', async () => {
      const closed = withIssue(fx.claimed, { state: 'closed' });
      const { calls } = mockApi({ ...reads(closed), 'PATCH /api/issues/2': {} });
      const { user } = renderView(<IssueView n={2} />);
      await user.click(await screen.findByText('Edit details'));
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() =>
        expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ allowClosed: true }),
      );
    });

    it('starts from sensible values for an untriaged issue', async () => {
      mockApi(reads(withIssue(fx.blocked, { priority: null, type: null, untriaged: true })));
      renderView(<IssueView n={3} />);
      await userEventOpen();
      expect(screen.getByLabelText('Type')).toHaveValue('task');
    });
  });

  it('shows comments as markdown', async () => {
    const d = withIssue(fx.claimed, {
      comments: [{ author: 'alice', createdAt: '2026-01-01T00:00:00Z', body: 'Looks **good**' }],
      commentsTotal: 5,
    });
    mockApi(reads(d));
    renderView(<IssueView n={2} />);
    expect(await screen.findByRole('heading', { name: /Comments/ })).toHaveTextContent('· 5');
    expect(screen.getByText('good').tagName).toBe('STRONG');
    expect(screen.getByText(/^alice · /)).toBeInTheDocument();
  });

  it('counts the comments it has when no total is given', async () => {
    const d = withIssue(fx.claimed, {
      comments: [{ author: 'a', createdAt: '2026-01-01T00:00:00Z', body: 'x' }],
      commentsTotal: undefined,
    });
    mockApi(reads(d));
    renderView(<IssueView n={2} />);
    expect(await screen.findByRole('heading', { name: /Comments/ })).toHaveTextContent('· 1');
  });
});

async function userEventOpen() {
  const { default: userEvent } = await import('@testing-library/user-event');
  await userEvent.click(await screen.findByText('Edit details'));
}
