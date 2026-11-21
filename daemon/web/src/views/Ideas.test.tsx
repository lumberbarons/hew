import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { fx } from '../test/fixtures';
import { mockApi, status } from '../test/mockApi';
import { renderView } from '../test/render';
import type { Issue } from '../types';
import { Ideas } from './Ideas';

const idea = (over: Partial<Issue> = {}): Issue => ({
  ...fx.claimed.issue,
  number: 9,
  title: 'Consider a cache',
  type: 'idea',
  priority: 'P2',
  assignees: [],
  inProgress: false,
  ...over,
});

describe('Ideas', () => {
  it('lists the open ideas the shared type filter returns', async () => {
    mockApi({ 'GET /api/issues?state=open&type=idea': [idea()] });
    renderView(<Ideas state="open" />);
    expect(await screen.findByRole('heading', { name: /Open ideas/ })).toHaveTextContent('· 1');
    expect(screen.getByRole('link', { name: 'Consider a cache' })).toHaveAttribute(
      'href',
      '#/issue/9',
    );
  });

  it('shows the empty state and links New idea to the form with Idea preselected', async () => {
    mockApi({ 'GET /api/issues?state=open&type=idea': [] });
    renderView(<Ideas state="open" />);
    expect(await screen.findByText('No open ideas. Capture one above.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'New idea' })).toHaveAttribute(
      'href',
      '#/new?type=idea',
    );
  });

  it('renders closed ideas and marks Closed as the current state', async () => {
    mockApi({ 'GET /api/issues?state=closed&type=idea': [idea({ state: 'closed' })] });
    renderView(<Ideas state="closed" />);
    expect(await screen.findByRole('heading', { name: /Closed ideas/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Consider a cache' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Closed' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Open' })).not.toHaveAttribute('aria-current');
  });

  it('says what failed, in the daemon words', async () => {
    mockApi({
      'GET /api/issues?state=open&type=idea': status(500, {
        error: 'internal',
        message: 'disk on fire',
      }),
    });
    renderView(<Ideas state="open" />);
    expect(await screen.findByText('disk on fire')).toBeInTheDocument();
  });
});
