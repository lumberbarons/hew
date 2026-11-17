import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';
import { setActingAs } from '../api';

// jsdom does not implement scrolling; the router asks for the top of the page.
window.scrollTo = () => {};

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  setActingAs(undefined);
  window.location.hash = '';
  localStorage.clear();
});
