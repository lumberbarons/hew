import { screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { fx } from '../test/fixtures';
import { mockApi, status } from '../test/mockApi';
import { renderView } from '../test/render';
import { Board } from './Board';
import { Primer } from './Primer';
import { Triage } from './Triage';

describe('Board', () => {
  it('lists epics first, then ready, in progress and blocked work', async () => {
    mockApi({ 'GET /api/board': fx.board });
    renderView(<Board />);
    expect(await screen.findByRole('heading', { name: /Epics/ })).toBeInTheDocument();
    const headings = screen
      .getAllByRole('heading', { level: 2 })
      .map((h) => h.textContent?.split(' ·')[0]);
    expect(headings).toEqual(['Epics', 'Ready', 'In progress', 'Blocked']);
    expect(screen.getByRole('link', { name: 'Crash on empty input' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Add dark mode' })).toBeInTheDocument();
  });

  it('warns about a dependency cycle and points at untriaged work without showing it', async () => {
    mockApi({ 'GET /api/board': fx.board });
    renderView(<Board />);
    expect(await screen.findByText(/#7 → #8 → #7/)).toBeInTheDocument();
    expect(screen.getByText(/1 untriaged issue hidden/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open triage' })).toHaveAttribute('href', '#/triage');
    expect(screen.queryByText('raw unvetted report')).toBeNull();
  });

  it('pluralises the untriaged note and omits it at zero', async () => {
    mockApi({ 'GET /api/board': { ...fx.board, untriaged: 3, warnings: [] } });
    const { unmount } = renderView(<Board />);
    expect(await screen.findByText(/3 untriaged issues hidden/)).toBeInTheDocument();
    unmount();
    mockApi({ 'GET /api/board': { ...fx.board, untriaged: 0 } });
    renderView(<Board />);
    await screen.findByRole('heading', { name: /Epics/ });
    expect(screen.queryByText(/untriaged/)).toBeNull();
  });

  it('says what failed, in the daemon words', async () => {
    mockApi({ 'GET /api/board': status(500, { error: 'internal', message: 'disk on fire' }) });
    renderView(<Board />);
    expect(await screen.findByText('disk on fire')).toBeInTheDocument();
  });
});

describe('Triage', () => {
  it('warns that its contents are unvetted, then lists them', async () => {
    mockApi({ 'GET /api/triage': fx.triage });
    renderView(<Triage />);
    expect(screen.getByText(/no maintainer has vetted/)).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'raw unvetted report' })).toBeInTheDocument();
  });

  it('shows the hidden-character findings the endpoint reports, naming the issue', async () => {
    const finding = {
      issue: 5,
      source: 'body',
      comment: 0,
      zeroWidth: true,
      bidiControl: true,
      unicodeTags: false,
      confusable: false,
    };
    mockApi({ 'GET /api/triage': { ...fx.triage, textFindings: [finding] } });
    renderView(<Triage />);
    expect(
      await screen.findByText('#5: Unusual characters in body: zeroWidth, bidiControl'),
    ).toBeInTheDocument();
  });

  it('shows no findings note when there are none', async () => {
    mockApi({ 'GET /api/triage': fx.triage });
    renderView(<Triage />);
    await screen.findByRole('link', { name: 'raw unvetted report' });
    expect(screen.queryByText(/Unusual characters/)).toBeNull();
  });

  it('says when there is nothing to triage', async () => {
    mockApi({ 'GET /api/triage': { ...fx.triage, issues: [] } });
    renderView(<Triage />);
    expect(await screen.findByText('Nothing to triage.')).toBeInTheDocument();
  });
});

describe('Primer', () => {
  it('shows what an agent session would be shown', async () => {
    mockApi({ 'GET /api/primer': fx.primer });
    renderView(<Primer />);
    expect(await screen.findByText(/9 open, 1 untriaged/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/Workflow: hew ready/)).toBeInTheDocument());
    expect(screen.getByRole('heading', { name: 'Conventions' })).toBeInTheDocument();
    expect(screen.getByText(/#7 → #8 → #7/)).toBeInTheDocument();
  });
});
