import { useCallback, useEffect, useState } from 'react';

export interface Loaded<T> {
  data?: T;
  error?: unknown;
  loading: boolean;
  /** Fetches again, keeping the current data on screen until it arrives. */
  reload: () => void;
}

/**
 * Runs load on mount and on every reload. Views are keyed by route, so a
 * different issue remounts rather than reusing the previous issue's data.
 */
export function useLoad<T>(load: () => Promise<T>): Loaded<T> {
  const [state, setState] = useState<{ data?: T; error?: unknown; loading: boolean }>({
    loading: true,
  });
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    let live = true;
    load().then(
      (data) => live && setState({ data, loading: false }),
      (error: unknown) => live && setState({ error, loading: false }),
    );
    return () => {
      live = false;
    };
    // load is an inline closure over the view's own props; tick is the trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick]);

  return { ...state, reload };
}
