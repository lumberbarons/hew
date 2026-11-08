/**
 * Integer-cent money helpers. Everything here is pure so callers can compose
 * totals without worrying about float drift.
 */

export type Cents = number;

/** Renders cents as a fixed two-decimal string, e.g. 1234 -> "12.34". */
export function formatCents(cents: Cents): string {
  const sign = cents < 0 ? "-" : "";
  const abs = Math.abs(cents);
  return `${sign}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, "0")}`;
}

// Number() accepts far more than a money string should — "", "0x1f", "1e3" and
// whitespace all coerce to a finite value — so the shape is checked first.
const DECIMAL = /^-?\d+(\.\d+)?$/;

/** Parses a decimal string into cents, rounding half away from zero. */
export function parseCents(input: string): Cents {
  const trimmed = input.trim();
  if (!DECIMAL.test(trimmed)) {
    throw new RangeError(`not a decimal amount: ${JSON.stringify(input)}`);
  }
  // Work on the digits, not on Number(trimmed) * 100: "1.005" has no exact binary
  // form, so scaling it as a float lands just below the half-cent and rounds down.
  const negative = trimmed.startsWith("-");
  const [whole, fraction = ""] = trimmed.replace("-", "").split(".");
  let cents = Number(whole) * 100 + Number((fraction + "00").slice(0, 2));
  if (fraction.length > 2 && fraction[2] >= "5") {
    cents += 1;
  }
  if (!Number.isSafeInteger(cents)) {
    throw new RangeError(`amount is too large to represent exactly: ${input}`);
  }
  return negative && cents !== 0 ? -cents : cents;
}

/** Sums cents. */
export function addCents(...amounts: Cents[]): Cents {
  return amounts.reduce((total, amount) => total + amount, 0);
}

/** Applies a fractional rate, rounding half away from zero. */
export function applyRate(cents: Cents, rate: number): Cents {
  if (!Number.isFinite(rate) || rate < 0) {
    throw new RangeError(`rate must be a non-negative number: ${rate}`);
  }
  return Math.sign(cents) * Math.round(Math.abs(cents) * rate);
}
