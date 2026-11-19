import { screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { fx } from '../test/fixtures';
import { mockApi, status } from '../test/mockApi';
import { renderView } from '../test/render';
import { Search } from './Search';

describe('Search', () => {
  it('searches the terms it arrived with', async () => {
    const { calls } = mockApi({ 'GET /api/search?q=importer': fx.search });
    renderView(<Search initial="importer" />);
    expect(await screen.findByRole('link', { name: 'Wire up the importer' })).toBeInTheDocument();
    expect(calls).toHaveLength(1);
    expect(screen.getByLabelText('Search terms')).toHaveValue('importer');
  });

  it('searches again when the same terms are submitted twice', async () => {
    const { calls } = mockApi({ 'GET /api/search?q=importer': fx.search });
    const { user } = renderView(<Search initial="importer" />);
    expect(await screen.findByRole('link', { name: 'Wire up the importer' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls.map((c) => c.path)).toEqual(['/api/search?q=importer', '/api/search?q=importer']);
  });

  it('does nothing until there is something to search for', async () => {
    const { calls } = mockApi({});
    const { user } = renderView(<Search initial="" />);
    await user.click(screen.getByRole('button', { name: 'Search' }));
    await user.type(screen.getByLabelText('Search terms'), '   {Enter}');
    expect(calls).toHaveLength(0);
    expect(screen.queryByRole('heading', { name: /Results/ })).toBeNull();
  });

  it('encodes the terms and replaces the previous results', async () => {
    mockApi({
      'GET /api/search?q=a%20b%26c': { ...fx.search, issues: [] },
    });
    const { user } = renderView(<Search initial="" />);
    await user.type(screen.getByLabelText('Search terms'), 'a b&c{Enter}');
    expect(await screen.findByText('No triaged matches.')).toBeInTheDocument();
  });

  it('says when a result was capped', async () => {
    mockApi({ 'GET /api/search?q=x': { ...fx.search, total: 120, fetched: 100 } });
    renderView(<Search initial="x" />);
    expect(await screen.findByText('Showing 100 of 120 matches.')).toBeInTheDocument();
  });

  it('reports a failed search', async () => {
    mockApi({
      'GET /api/search?q=x': status(400, {
        error: 'invalid_input',
        message: 'bad',
        fields: { field: 'terms', reason: 'must not be empty' },
      }),
    });
    renderView(<Search initial="x" />);
    expect(await screen.findByText('terms: must not be empty')).toBeInTheDocument();
  });
});
