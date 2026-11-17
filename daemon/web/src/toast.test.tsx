import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { ToastProvider, useAct, useToast } from './toast';
import { fx } from './test/fixtures';

function Harness({ fail }: { fail?: boolean }) {
  const toast = useToast();
  const act = useAct();
  return (
    <>
      <button onClick={() => toast('Saved')}>ok</button>
      <button onClick={() => toast('Broken', 'bad')}>bad</button>
      <button
        onClick={() =>
          void act(
            () =>
              fail ? Promise.reject(new ApiError(409, fx.claimedByOther)) : Promise.resolve(1),
            'Done',
          )
        }
      >
        run
      </button>
      <button onClick={() => void act(() => Promise.resolve(1))}>quiet</button>
    </>
  );
}

const setup = (props = {}) => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  render(
    <ToastProvider>
      <Harness {...props} />
    </ToastProvider>,
  );
  return user;
};

describe('toasts', () => {
  it('shows a message and removes it after a few seconds', async () => {
    const user = setup();
    await user.click(screen.getByText('ok'));
    expect(screen.getByRole('status')).toHaveTextContent('Saved');
    act(() => void vi.advanceTimersByTime(3600));
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('keeps an error on screen longer than a success', async () => {
    const user = setup();
    await user.click(screen.getByText('bad'));
    act(() => void vi.advanceTimersByTime(3600));
    expect(screen.getByRole('status')).toHaveTextContent('Broken');
    act(() => void vi.advanceTimersByTime(3600));
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('is harmless without a provider', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByText('ok'));
    expect(screen.queryByRole('status')).toBeNull();
  });
});

describe('useAct', () => {
  it('announces success and returns the result', async () => {
    const user = setup();
    await user.click(screen.getByText('run'));
    expect(await screen.findByText('Done')).toBeInTheDocument();
  });

  it('says nothing on success when given no message', async () => {
    const user = setup();
    await user.click(screen.getByText('quiet'));
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('turns a failure into the explained sentence', async () => {
    const user = setup({ fail: true });
    await user.click(screen.getByText('run'));
    expect(
      await screen.findByText('#2 is claimed by alice. Pick the next ready item.'),
    ).toBeInTheDocument();
  });
});
