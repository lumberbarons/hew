import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { storeActor } from './api';
import { fx } from './test/fixtures';
import { mockApi, status } from './test/mockApi';
import { ToastProvider } from './toast';

const mount = () => {
  const user = userEvent.setup();
  return {
    user,
    ...render(
      <ToastProvider>
        <App />
      </ToastProvider>,
    ),
  };
};

const goto = (hash: string) =>
  act(() => {
    window.location.hash = hash;
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  });

const routes = {
  'GET /api/config': fx.config,
  'GET /api/board': fx.board,
};

describe('App', () => {
  it('waits for the config, then shows the board', async () => {
    mockApi(routes);
    mount();
    expect(await screen.findByRole('heading', { name: 'Board' })).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'Crash on empty input' })).toBeInTheDocument();
  });

  it('marks the current page in the nav, filing an issue under the board', async () => {
    mockApi({ ...routes, 'GET /api/issues/2': fx.claimed, 'GET /api/primer': fx.primer });
    mount();
    const current = () =>
      screen.getAllByRole('link', { current: 'page' }).map((a) => a.textContent);
    await screen.findByRole('heading', { name: 'Board' });
    expect(current()).toEqual(['Board']);

    await goto('#/issue/2');
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
      'Crash on empty input',
    );
    expect(current()).toEqual(['Board']);

    await goto('#/primer');
    expect(await screen.findByRole('heading', { name: 'Primer' })).toBeInTheDocument();
    expect(current()).toEqual(['Primer']);
  });

  it.each([
    ['#/new', 'New'],
    ['#/search', 'Search'],
    ['#/apply', 'Apply a plan'],
  ])('routes %s to its view', async (hash, heading) => {
    mockApi(routes);
    mount();
    await screen.findByRole('heading', { name: 'Board' });
    await goto(hash);
    expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
  });

  it('routes the triage page', async () => {
    mockApi({ ...routes, 'GET /api/triage': fx.triage });
    mount();
    await screen.findByRole('heading', { name: 'Board' });
    await goto('#/triage');
    expect(await screen.findByRole('link', { name: 'raw unvetted report' })).toBeInTheDocument();
  });

  it('opens a search with the terms in the URL', async () => {
    mockApi({ ...routes, 'GET /api/search?q=importer': fx.search });
    window.location.hash = '#/search?q=importer';
    mount();
    expect(await screen.findByRole('link', { name: 'Wire up the importer' })).toBeInTheDocument();
  });

  it('shows who a local tracker acts as, and lets you change it', async () => {
    const { calls } = mockApi(routes);
    const { user } = mount();
    const who = await screen.findByLabelText('Acting as');
    expect(who).toHaveValue('daemon'); // the tracker's own identity by default
    await waitFor(() => expect(calls.filter((c) => c.path === '/api/board')).toHaveLength(1));
    expect(calls.find((c) => c.path === '/api/board')?.headers['X-Hew-Actor']).toBeUndefined();

    await user.clear(who);
    await user.type(who, ' alice {Enter}');
    // Committing refetches the view as the new actor and remembers the choice.
    await waitFor(() => expect(calls.filter((c) => c.path === '/api/board')).toHaveLength(2));
    expect(calls.filter((c) => c.path === '/api/board').at(-1)?.headers['X-Hew-Actor']).toBe(
      'alice',
    );
    expect(localStorage.getItem('actor')).toBe('alice');
  });

  it('acts as the remembered actor from the first request', async () => {
    storeActor('bob');
    const { calls } = mockApi(routes);
    mount();
    await screen.findByRole('heading', { name: 'Board' });
    expect(
      calls
        .filter((c) => c.path !== '/api/config')
        .every((c) => c.headers['X-Hew-Actor'] === 'bob'),
    ).toBe(true);
    expect(screen.getByLabelText('Acting as')).toHaveValue('bob');
  });

  it('shows the authenticated login on GitHub, where the actor is not editable', async () => {
    storeActor('ignored');
    const { calls } = mockApi({
      ...routes,
      'GET /api/config': {
        ...fx.config,
        backend: 'github',
        repo: 'o/r',
        identity: 'octocat',
        actorEditable: false,
      },
    });
    mount();
    expect(await screen.findByText('github o/r · octocat')).toBeInTheDocument();
    expect(screen.queryByLabelText('Acting as')).toBeNull();
    await screen.findByRole('heading', { name: 'Board' });
    expect(calls.every((c) => c.headers['X-Hew-Actor'] === undefined)).toBe(true);
  });

  it('still shows the page when the config cannot be read', async () => {
    mockApi({
      'GET /api/config': status(500, { error: 'internal', message: 'config exploded' }),
      'GET /api/board': fx.board,
    });
    mount();
    expect(await screen.findByText('config exploded')).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'Board' })).toBeInTheDocument();
    expect(screen.queryByLabelText('Acting as')).toBeNull();
  });

  it('survives a config request that fails outright', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.reject(new Error('offline'))),
    );
    mount();
    expect(await screen.findByText('offline')).toBeInTheDocument();
  });
});
