import { act, renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { navHref, parseHash, useRoute } from './router';

describe('parseHash', () => {
  it('maps each view', () => {
    expect(parseHash('')).toEqual({ name: 'board' });
    expect(parseHash('#/')).toEqual({ name: 'board' });
    expect(parseHash('#/issue/12')).toEqual({ name: 'issue', n: 12 });
    expect(parseHash('#/new')).toEqual({ name: 'new' });
    expect(parseHash('#/triage')).toEqual({ name: 'triage' });
    expect(parseHash('#/apply')).toEqual({ name: 'apply' });
    expect(parseHash('#/primer')).toEqual({ name: 'primer' });
  });

  it('reads the search terms from the query', () => {
    expect(parseHash('#/search?q=import%20er')).toEqual({ name: 'search', q: 'import er' });
    expect(parseHash('#/search')).toEqual({ name: 'search', q: '' });
  });

  it('sends anything it cannot place to the board', () => {
    for (const hash of ['#/issue/abc', '#/issue/0', '#/issue/-4', '#/issue', '#/nowhere']) {
      expect(parseHash(hash)).toEqual({ name: 'board' });
    }
  });
});

describe('navHref', () => {
  it('files an issue page under the board', () => {
    expect(navHref({ name: 'issue', n: 3 })).toBe('#/');
    expect(navHref({ name: 'board' })).toBe('#/');
    expect(navHref({ name: 'new' })).toBe('#/new');
    expect(navHref({ name: 'search', q: 'x' })).toBe('#/search');
  });
});

describe('useRoute', () => {
  it('follows the location hash and returns to the top of the page', () => {
    const scroll = vi.spyOn(window, 'scrollTo').mockImplementation(() => {});
    window.location.hash = '#/new';
    const { result, unmount } = renderHook(() => useRoute());
    expect(result.current).toEqual({ name: 'new' });

    act(() => {
      window.location.hash = '#/issue/9';
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(result.current).toEqual({ name: 'issue', n: 9 });
    expect(scroll).toHaveBeenCalledWith(0, 0);

    unmount();
    scroll.mockClear();
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    expect(scroll).not.toHaveBeenCalled();
  });
});
