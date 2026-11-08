/** Pure money helpers. Deterministic, no I/O — callers report failures. */

export function toCents(amount: string): number {
  const parsed = Number(amount);
  if (!Number.isFinite(parsed)) {
    throw new RangeError(`amount is not a number: ${JSON.stringify(amount)}`);
  }
  return Math.round(parsed * 100);
}

export function applyTaxCents(subtotalCents: number, rate: number): number {
  if (rate < 0 || rate > 1) {
    throw new RangeError(`tax rate out of range [0,1]: ${rate}`);
  }
  return Math.round(subtotalCents * (1 + rate));
}

export function formatCents(cents: number): string {
  return (cents / 100).toFixed(2);
}
