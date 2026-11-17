import type { Problem } from './types';

/** An error response from the daemon, carrying its stable kind and fields. */
export class ApiError extends Error {
  readonly status: number;
  readonly kind: string;
  readonly fields: Record<string, unknown>;

  constructor(status: number, body: Partial<Problem>) {
    super(body.message ?? `HTTP ${status}`);
    this.status = status;
    this.kind = body.error ?? 'internal';
    this.fields = body.fields ?? {};
  }
}

let actingAs: string | undefined;

/**
 * Sets who requests act as. On a local tracker the daemon reads this from the
 * X-Hew-Actor header; on GitHub every request is the authenticated login, so
 * the caller passes undefined there.
 */
export function setActingAs(actor: string | undefined): void {
  actingAs = actor?.trim() || undefined;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (actingAs) headers['X-Hew-Actor'] = actingAs;
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data: unknown = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data as Partial<Problem>);
  return data as T;
}

const nums = (v: unknown): string =>
  (Array.isArray(v) ? v : []).map((n: unknown) => `#${String(n)}`).join(', ');

const list = (v: unknown): string => (Array.isArray(v) ? v.join(', ') : '');

/**
 * The CLI's exit-code contract as sentences: what an agent loop does on a
 * claimed-by-other, claimed-by-you or blocked refusal.
 */
export function explain(e: unknown): string {
  if (!(e instanceof ApiError)) return e instanceof Error ? e.message : String(e);
  const f = e.fields;
  switch (e.kind) {
    case 'claimed_by_other':
      return `#${String(f.issue)} is claimed by ${list(f.assignees) || 'someone else'}. Pick the next ready item.`;
    case 'claimed_by_you':
      return `#${String(f.issue)} is already claimed by you. Resume that work.`;
    case 'blocked':
      return `#${String(f.issue)} has open blockers: ${nums(f.openBlockers)}. Pick another item.`;
    case 'untriaged':
      return `#${String(f.issue)} is untriaged. Claiming it is triage: choose a priority.`;
    case 'epic':
      return `#${String(f.issue)} is an epic. Work its children instead.`;
    case 'cycle':
      return `That would create a dependency cycle: ${(Array.isArray(f.cycle) ? f.cycle : []).map((n: unknown) => `#${String(n)}`).join(' → ')}.`;
    case 'invalid_input':
      return `${String(f.field)}: ${String(f.reason)}`;
    case 'not_owner':
      return `#${String(f.issue)} is not claimed solely by ${String(f.actor)}.`;
    case 'lock_timeout':
      return 'The local tracker is locked by another process. Try again.';
    default:
      return e.message;
  }
}

/** Reads the remembered actor. Storage can be blocked, so it may be absent. */
export function storedActor(): string {
  try {
    return localStorage.getItem('actor') ?? '';
  } catch {
    return '';
  }
}

export function storeActor(actor: string): void {
  try {
    localStorage.setItem('actor', actor);
  } catch {
    /* storage may be blocked; the page works without it */
  }
}
