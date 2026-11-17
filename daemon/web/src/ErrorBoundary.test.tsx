import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Boom(): never {
  throw new Error('areas is undefined');
}

describe('ErrorBoundary', () => {
  it('shows a failed render as a message with a way back', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('areas is undefined');
    expect(screen.getByRole('link', { name: 'Back to the board' })).toHaveAttribute('href', '#/');
  });

  it('renders its children when nothing fails', () => {
    render(
      <ErrorBoundary>
        <p>fine</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('fine')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('recovers when it is remounted for another page', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const { rerender } = render(
      <ErrorBoundary key="a">
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByRole('alert')).toBeInTheDocument();
    rerender(
      <ErrorBoundary key="b">
        <p>next page</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('next page')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
