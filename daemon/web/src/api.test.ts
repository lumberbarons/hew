import { describe, expect, it, vi } from 'vitest';
import { ApiError, api, explain, setActingAs, storeActor, storedActor } from './api';
import { fx } from './test/fixtures';
import { mockApi, status } from './test/mockApi';

describe('api', () => {
  it('returns the parsed body of a successful call', async () => {
    mockApi({ 'GET /api/config': fx.config });
    await expect(api('GET', '/api/config')).resolves.toEqual(fx.config);
  });

  it('sends a JSON body with the content type the daemon requires', async () => {
    const { calls } = mockApi({ 'POST /api/x': {} });
    await api('POST', '/api/x', { a: 1 });
    expect(calls[0]?.body).toEqual({ a: 1 });
    expect(calls[0]?.headers['Content-Type']).toBe('application/json');
  });

  it('sends the acting identity only once one is set', async () => {
    const { calls } = mockApi({ 'GET /api/x': {} });
    await api('GET', '/api/x');
    setActingAs('  alice ');
    await api('GET', '/api/x');
    setActingAs('   ');
    await api('GET', '/api/x');
    expect(calls.map((c) => c.headers['X-Hew-Actor'])).toEqual([undefined, 'alice', undefined]);
  });

  it('turns an error response into an ApiError carrying kind and fields', async () => {
    mockApi({ 'POST /api/x': status(409, fx.claimedByOther) });
    const err = await api('POST', '/api/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 409, kind: 'claimed_by_other', fields: { issue: 2 } });
  });

  it('survives an error response that is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(new Response('<html>bad gateway</html>', { status: 502 }))),
    );
    const err = (await api('GET', '/api/x').catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
    expect(err.kind).toBe('internal');
    expect(err.message).toBe('HTTP 502');
  });
});

describe('explain', () => {
  const problem = (error: string, fields: Record<string, unknown>) =>
    new ApiError(409, { error, message: `raw ${error}`, fields });

  it('turns the CLI exit-code outcomes into the sentence an agent acts on', () => {
    expect(explain(new ApiError(409, fx.claimedByOther))).toBe(
      '#2 is claimed by alice. Pick the next ready item.',
    );
    expect(explain(new ApiError(409, fx.claimedByYou))).toBe(
      '#2 is already claimed by you. Resume that work.',
    );
    expect(explain(new ApiError(409, fx.blockedError))).toBe(
      '#3 has open blockers: #1. Pick another item.',
    );
  });

  it('names the claimant as someone else when none is listed', () => {
    expect(explain(problem('claimed_by_other', { issue: 4, assignees: [] }))).toContain(
      'someone else',
    );
  });

  it('explains the remaining kinds', () => {
    expect(explain(problem('untriaged', { issue: 5 }))).toContain('choose a priority');
    expect(explain(problem('epic', { issue: 5 }))).toContain('Work its children');
    expect(explain(problem('cycle', { cycle: [1, 2, 1] }))).toContain('#1 → #2 → #1');
    expect(explain(problem('cycle', {}))).toContain('cycle');
    expect(explain(new ApiError(400, fx.invalid))).toBe('reason: is required');
    expect(explain(problem('not_owner', { issue: 5, actor: 'bob' }))).toBe(
      '#5 is not claimed solely by bob.',
    );
    expect(explain(problem('lock_timeout', {}))).toContain('locked');
  });

  it('falls back to the server message for an unlisted kind', () => {
    expect(explain(problem('closed', {}))).toBe('raw closed');
  });

  it('describes errors that did not come from the daemon', () => {
    expect(explain(new Error('network down'))).toBe('network down');
    expect(explain('plain string')).toBe('plain string');
  });
});

describe('stored actor', () => {
  it('round-trips through storage', () => {
    expect(storedActor()).toBe('');
    storeActor('alice');
    expect(storedActor()).toBe('alice');
  });

  it('works without storage, which a private window can refuse', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    expect(storedActor()).toBe('');
    expect(() => storeActor('alice')).not.toThrow();
  });
});
