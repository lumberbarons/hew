import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { fx } from '../test/fixtures';
import { mockApi, status } from '../test/mockApi';
import { renderView } from '../test/render';
import { Apply } from './Apply';

describe('Apply', () => {
  it('starts from an example plan', () => {
    mockApi({});
    renderView(<Apply />);
    const plan = JSON.parse((screen.getByLabelText('Plan') as HTMLTextAreaElement).value);
    expect(plan.entries).toHaveLength(3);
  });

  it('dry-runs the plan and shows what it would do', async () => {
    const { calls } = mockApi({ 'POST /api/apply?dryRun=1': fx.apply });
    const { user } = renderView(<Apply />);
    await user.click(screen.getByRole('button', { name: 'Dry run' }));
    expect(await screen.findByRole('heading', { name: 'Dry run result' })).toBeInTheDocument();
    expect(screen.getByText(/"dryRun": true/)).toBeInTheDocument();
    expect((calls[0]?.body as { entries: unknown[] }).entries).toHaveLength(3);
  });

  it('applies the plan for real', async () => {
    mockApi({ 'POST /api/apply': { ...fx.apply, dryRun: false, created: 3 } });
    const { user } = renderView(<Apply />);
    await user.click(screen.getByRole('button', { name: 'Apply' }));
    expect(await screen.findByRole('heading', { name: 'Result' })).toBeInTheDocument();
    expect(screen.getByText(/"created": 3/)).toBeInTheDocument();
  });

  it('refuses a plan that is not JSON without calling the daemon', async () => {
    const { calls } = mockApi({});
    const { user } = renderView(<Apply />);
    fireEvent.change(screen.getByLabelText('Plan'), { target: { value: '{ nope' } });
    await user.click(screen.getByRole('button', { name: 'Dry run' }));
    expect(await screen.findByText(/Not valid JSON/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it('explains a plan the daemon refuses and shows no result', async () => {
    mockApi({
      'POST /api/apply': status(400, {
        error: 'invalid_input',
        message: 'x',
        fields: { field: 'entries', reason: 'dangling reference' },
      }),
    });
    const { user } = renderView(<Apply />);
    await user.click(screen.getByRole('button', { name: 'Apply' }));
    expect(await screen.findByText('entries: dangling reference')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Result' })).toBeNull();
  });

  it('cannot be sent twice while a plan is running', async () => {
    let finish: (r: Response) => void = () => {};
    vi.stubGlobal(
      'fetch',
      vi.fn(() => new Promise<Response>((resolve) => (finish = resolve))),
    );
    const { user } = renderView(<Apply />);
    await user.click(screen.getByRole('button', { name: 'Apply' }));
    expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Dry run' })).toBeDisabled();

    finish(new Response(JSON.stringify(fx.apply)));
    expect(await screen.findByRole('heading', { name: /Result/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled();
  });
});
