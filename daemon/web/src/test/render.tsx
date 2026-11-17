import { render } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactElement } from 'react';
import { ToastProvider } from '../toast';

/** Renders a view with toasts available, and a user to drive it. */
export function renderView(ui: ReactElement) {
  const user = userEvent.setup();
  return { user, ...render(<ToastProvider>{ui}</ToastProvider>) };
}
