import { Component, type ErrorInfo, type ReactNode } from 'react';

interface State {
  error?: Error;
}

/**
 * Shows a failed render as a message instead of an empty page. Every list the
 * UI reads is meant to arrive as [] and never null, but the guarantee lives in
 * the Go handlers; one that slips turns a TypeError into a blank document with
 * no navigation. The boundary leaves the header, so the person can go back.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = {};

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('The page failed to render:', error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <div className="note bad" role="alert">
          This page could not be shown: {this.state.error.message}.{' '}
          <a href="#/">Back to the board</a>
        </div>
      );
    }
    return this.props.children;
  }
}
