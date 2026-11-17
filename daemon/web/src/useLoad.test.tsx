import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { useLoad } from './useLoad';

describe('useLoad', () => {
  it('loads on mount', async () => {
    const { result } = renderHook(() => useLoad(() => Promise.resolve('hello')));
    expect(result.current.loading).toBe(true);
    await waitFor(() => expect(result.current.data).toBe('hello'));
    expect(result.current.loading).toBe(false);
  });

  it('reports a failure instead of data', async () => {
    const { result } = renderHook(() => useLoad(() => Promise.reject(new Error('nope'))));
    await waitFor(() => expect(result.current.error).toEqual(new Error('nope')));
    expect(result.current.data).toBeUndefined();
  });

  it('fetches again on reload, keeping the old data until the new arrives', async () => {
    let n = 0;
    const load = vi.fn(() => Promise.resolve(++n));
    const { result } = renderHook(() => useLoad(load));
    await waitFor(() => expect(result.current.data).toBe(1));
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.data).toBe(2));
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('drops a result that arrives after the view is gone', async () => {
    let resolve: (v: string) => void = () => {};
    const { result, unmount } = renderHook(() =>
      useLoad(() => new Promise<string>((r) => (resolve = r))),
    );
    unmount();
    resolve('late');
    await Promise.resolve();
    expect(result.current.data).toBeUndefined();
  });
});
