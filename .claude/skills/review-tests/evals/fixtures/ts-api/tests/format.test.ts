import { describe, it, expect } from "vitest";
import { formatCents, truncate } from "../src/format";

describe("formatCents", () => {
  it.each([
    [0, "$0.00"],
    [5, "$0.05"],
    [100, "$1.00"],
    [12345, "$123.45"],
    [-250, "-$2.50"],
  ])("formats %i cents as %s", (cents, expected) => {
    expect(formatCents(cents)).toBe(expected);
  });
});

describe("truncate", () => {
  it("leaves a string shorter than the limit untouched", () => {
    expect(truncate("hello", 10)).toBe("hello");
  });

  it("leaves a string exactly at the limit untouched", () => {
    expect(truncate("hello", 5)).toBe("hello");
  });

  it("replaces the final character with an ellipsis when over the limit", () => {
    expect(truncate("hello world", 5)).toBe("hell…");
  });

  it("returns an empty string for a non-positive limit", () => {
    expect(truncate("hello", 0)).toBe("");
  });
});
