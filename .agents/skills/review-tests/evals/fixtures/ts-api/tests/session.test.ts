import { describe, it, expect, vi } from "vitest";
import { endSession } from "../src/client";

// Installed once at import time, shared by every test in this file.
const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

describe("endSession", () => {
  it("ends the session", async () => {
    mockFetch.mockResolvedValue({ status: 204 });
    const ok = await endSession("tok");
    expect(ok).toBe(true);
  });
});
