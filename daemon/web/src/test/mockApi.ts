import { vi } from 'vitest';

export interface Call {
  method: string;
  path: string;
  body: unknown;
  headers: Record<string, string>;
}

/** A reply with a non-200 status. */
export const status = (code: number, body: unknown) => ({ __status: code, body });
const isStatus = (v: unknown): v is ReturnType<typeof status> =>
  typeof v === 'object' && v !== null && '__status' in v;

type Reply = unknown | ((call: Call) => unknown);

/**
 * Stands in for fetch. Routes are keyed "METHOD /path?query"; a request with no
 * route is an error, so a test cannot pass by accident on a call it did not
 * expect. Returns every call made, in order.
 */
export function mockApi(routes: Record<string, Reply>): { calls: Call[] } {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      const path = String(input);
      const call: Call = {
        method,
        path,
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
        headers: (init?.headers ?? {}) as Record<string, string>,
      };
      calls.push(call);
      const key = `${method} ${path}`;
      if (!(key in routes)) return Promise.reject(new Error(`unmocked request: ${key}`));
      let reply = routes[key];
      if (typeof reply === 'function') reply = (reply as (c: Call) => unknown)(call);
      const code = isStatus(reply) ? reply.__status : 200;
      const payload = isStatus(reply) ? reply.body : reply;
      return Promise.resolve(new Response(JSON.stringify(payload), { status: code }));
    }),
  );
  return { calls };
}
