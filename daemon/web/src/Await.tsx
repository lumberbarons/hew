import type { ReactNode } from 'react';
import { explain } from './api';
import type { Loaded } from './useLoad';

/** Renders a loaded value, or the explained error, or nothing while loading. */
export function Await<T>({
  state,
  children,
}: {
  state: Loaded<T>;
  children: (data: T, reload: () => void) => ReactNode;
}) {
  if (state.error !== undefined) {
    return <div className="note bad">{explain(state.error)}</div>;
  }
  if (state.data === undefined) return null;
  return <>{children(state.data, state.reload)}</>;
}
