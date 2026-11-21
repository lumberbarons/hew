import { screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { fx } from '../test/fixtures';
import { mockApi, status } from '../test/mockApi';
import { renderView } from '../test/render';
import { buildPayload, NewForm, type FormState } from './NewForm';

const base: FormState = {
  kind: 'issue',
  title: 'T',
  type: 'task',
  prio: 'P2',
  areas: '',
  where: '',
  problem: 'p',
  fix: 'f',
  questions: '',
  done: '',
  blockedBy: '',
  parent: '',
  children: '',
};

describe('buildPayload', () => {
  it('builds an issue with Goal and Approach for a task', () => {
    const { path, body } = buildPayload({
      ...base,
      areas: ' cli , docs,, ',
      blockedBy: '#3, 4',
      parent: '#12',
      done: ' a \n\n b ',
    });
    expect(path).toBe('/api/issues');
    expect(body).toEqual({
      title: 'T',
      type: 'task',
      priority: 'P2',
      areas: ['cli', 'docs'],
      parent: 12,
      blockedBy: [3, 4],
      body: { where: '', doneWhen: ['a', 'b'], goal: 'p', approach: 'f' },
    });
  });

  it('uses Problem and Fix for a bug', () => {
    const { body } = buildPayload({ ...base, type: 'bug' });
    expect(body.body).toEqual({ where: '', doneWhen: [], problem: 'p', fix: 'f' });
  });

  it('composes an idea from a problem and its open questions', () => {
    const { body } = buildPayload({
      ...base,
      type: 'idea',
      where: 'ignored',
      fix: 'not an idea field',
      done: 'ignored too',
      questions: ' How long does it stay hot? \n\n second question ',
    });
    expect(body.body).toEqual({
      problem: 'p',
      openQuestions: ['How long does it stay hot?', 'second question'],
    });
  });

  it('omits Open questions for an idea that has none', () => {
    const { body } = buildPayload({ ...base, type: 'idea' });
    expect(body.body).toEqual({ problem: 'p' });
  });

  it('builds an epic with its children and no issue-only fields', () => {
    const { path, body } = buildPayload({
      ...base,
      kind: 'epic',
      type: 'bug',
      children: '#1 #2,3',
    });
    expect(path).toBe('/api/epics');
    expect(body).toEqual({
      title: 'T',
      children: [1, 2, 3],
      body: { where: '', doneWhen: [], goal: 'p', approach: 'f' },
    });
  });

  it('sends no parent when none is given', () => {
    expect(buildPayload(base).body.parent).toBe(0);
  });
});

describe('NewForm', () => {
  it('stays quiet until the person types, then validates through the dry run', async () => {
    const { calls } = mockApi({ 'POST /api/issues?dryRun=1': fx.check });
    const { user } = renderView(<NewForm />);
    expect(calls).toHaveLength(0);
    expect(screen.queryByText('Ready to create.')).toBeNull();
    await user.type(screen.getByLabelText('Title'), 'Tidy the importer');
    expect(await screen.findByText('Ready to create.')).toBeInTheDocument();
    expect(calls.every((c) => c.path === '/api/issues?dryRun=1')).toBe(true);
    expect((calls.at(-1)?.body as { title: string }).title).toBe('Tidy the importer');
  });

  it('passes the library advice about unmarked code through', async () => {
    mockApi({ 'POST /api/issues?dryRun=1': fx.check });
    const { user } = renderView(<NewForm />);
    await user.type(screen.getByLabelText('Title'), 'x');
    expect(await screen.findByText('Wrap in backticks: internal/cli/pr.go')).toBeInTheDocument();
  });

  it('shows the write path refusing a bad reference as it is typed', async () => {
    mockApi({
      'POST /api/issues?dryRun=1': status(404, {
        error: 'not_found',
        message: 'issue #99 not found',
        fields: { issue: 99 },
      }),
    });
    const { user } = renderView(<NewForm />);
    await user.type(screen.getByLabelText('Blocked by'), '#99');
    expect(await screen.findByText('issue #99 not found')).toBeInTheDocument();
    expect(screen.queryByText('Ready to create.')).toBeNull();
  });

  it('shows only the fields that apply to an epic', async () => {
    mockApi({ 'POST /api/epics?dryRun=1': { ok: true, findings: [] } });
    const { user } = renderView(<NewForm />);
    expect(screen.getByLabelText('Goal')).toBeInTheDocument();
    expect(screen.getByLabelText('Blocked by')).toBeVisible();
    expect(screen.queryByLabelText('Children')).toBeNull();

    await user.selectOptions(screen.getByLabelText('Kind'), 'epic');
    expect(screen.getByLabelText('Children')).toBeVisible();
    expect(screen.queryByLabelText('Blocked by')).toBeNull();
    expect(screen.getByLabelText('Type', { selector: 'select' })).not.toBeVisible();
    expect(screen.getByLabelText('Priority', { selector: 'select' })).not.toBeVisible();
    expect(screen.getByLabelText('Areas')).not.toBeVisible();
    expect(await screen.findByText('Ready to create.')).toBeInTheDocument();
  });

  it('relabels the description for a bug', async () => {
    mockApi({ 'POST /api/issues?dryRun=1': fx.check });
    const { user } = renderView(<NewForm />);
    await user.selectOptions(screen.getByLabelText('Type'), 'bug');
    expect(screen.getByLabelText('Problem')).toBeInTheDocument();
    expect(screen.getByLabelText('Fix')).toBeInTheDocument();
    expect(screen.queryByLabelText('Goal')).toBeNull();
  });

  it('preselects Idea from the link and shows only the idea fields', () => {
    mockApi({});
    renderView(<NewForm type="idea" />);
    expect(screen.getByLabelText('Type')).toHaveValue('idea');
    expect(screen.getByLabelText('Problem or opportunity')).toBeVisible();
    expect(screen.getByLabelText('Open questions')).toBeVisible();
    expect(screen.getByLabelText('Where')).not.toBeVisible();
    expect(screen.getByLabelText('Approach')).not.toBeVisible();
    expect(screen.getByLabelText('Done when')).not.toBeVisible();
    expect(screen.getByLabelText('Parent epic')).not.toBeVisible();
  });

  it('creates an idea with its statement and questions', async () => {
    const { calls } = mockApi({
      'POST /api/issues?dryRun=1': fx.check,
      'POST /api/issues': { ...fx.create, issue: { ...fx.create.issue, number: 9 } },
    });
    const { user } = renderView(<NewForm type="idea" />);
    await user.type(screen.getByLabelText('Title'), 'Consider a cache');
    await user.type(screen.getByLabelText('Problem or opportunity'), 'Repeated reads are slow');
    await user.type(screen.getByLabelText('Open questions'), 'How often does it change?');
    expect(await screen.findByText('Ready to create.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(window.location.hash).toBe('#/issue/9'));
    expect(calls.find((c) => c.path === '/api/issues')?.body).toMatchObject({
      type: 'idea',
      body: {
        problem: 'Repeated reads are slow',
        openQuestions: ['How often does it change?'],
      },
    });
  });

  it('falls back to the default type when the link names an unknown one', () => {
    mockApi({});
    renderView(<NewForm type="spike" />);
    expect(screen.getByLabelText('Type')).toHaveValue('task');
  });

  it('creates the issue and opens it', async () => {
    const { calls } = mockApi({
      'POST /api/issues?dryRun=1': fx.check,
      'POST /api/issues': { ...fx.create, issue: { ...fx.create.issue, number: 9 } },
    });
    const { user } = renderView(<NewForm />);
    await user.type(screen.getByLabelText('Title'), 'Fixture issue');
    await user.type(screen.getByLabelText('Done when'), 'It works');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(window.location.hash).toBe('#/issue/9'));
    const created = calls.find((c) => c.path === '/api/issues');
    expect(created?.body).toMatchObject({
      title: 'Fixture issue',
      body: { doneWhen: ['It works'] },
    });
    expect(await screen.findByText('Created')).toBeInTheDocument();
  });

  it('stays on the form when the daemon refuses', async () => {
    mockApi({
      'POST /api/issues?dryRun=1': fx.check,
      'POST /api/issues': status(400, {
        error: 'invalid_input',
        message: 'x',
        fields: { field: 'title', reason: 'must not be empty' },
      }),
    });
    const { user } = renderView(<NewForm />);
    await user.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByText('title: must not be empty')).toBeInTheDocument();
    expect(window.location.hash).toBe('');
    expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled();
  });

  it('cancels back to the board', async () => {
    mockApi({});
    const { user } = renderView(<NewForm />);
    window.location.hash = '#/new';
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(window.location.hash).toBe('#/');
  });

  it('sends every field it collects', async () => {
    const { calls } = mockApi({
      'POST /api/issues?dryRun=1': fx.check,
      'POST /api/epics?dryRun=1': fx.check,
    });
    const { user } = renderView(<NewForm />);
    await user.selectOptions(screen.getByLabelText('Priority'), 'P0');
    await user.type(screen.getByLabelText('Title'), 'Everything');
    await user.type(screen.getByLabelText('Areas'), 'cli, docs');
    await user.type(screen.getByLabelText('Where'), 'cmd/hew');
    await user.type(screen.getByLabelText('Goal'), 'Be complete');
    await user.type(screen.getByLabelText('Approach'), 'Fill it in');
    await user.type(screen.getByLabelText('Done when'), 'one{Enter}two');
    await user.type(screen.getByLabelText('Blocked by'), '3');
    await user.type(screen.getByLabelText('Parent epic'), '12');
    await waitFor(() =>
      expect(calls.at(-1)?.body).toEqual({
        title: 'Everything',
        type: 'task',
        priority: 'P0',
        areas: ['cli', 'docs'],
        parent: 12,
        blockedBy: [3],
        body: {
          where: 'cmd/hew',
          goal: 'Be complete',
          approach: 'Fill it in',
          doneWhen: ['one', 'two'],
        },
      }),
    );

    await user.selectOptions(screen.getByLabelText('Kind'), 'epic');
    await user.type(screen.getByLabelText('Children'), '1, 2');
    await waitFor(() =>
      expect(calls.at(-1)).toMatchObject({
        path: '/api/epics?dryRun=1',
        body: { title: 'Everything', children: [1, 2] },
      }),
    );
  });
});
