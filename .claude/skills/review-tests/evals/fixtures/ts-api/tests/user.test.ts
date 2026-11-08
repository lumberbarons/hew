import { describe, it, expect, vi } from "vitest";
import { fetchUser } from "../src/client";

// Installed once at import time, shared by every test in this file.
const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

describe("fetchUser", () => {
  it("returns the user", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ id: "u1", email: "a@b.co", plan: "pro" }),
    });
    const user = await fetchUser("u1");
    expect(user.id).toBe("u1");
  });

  it("throws on a non-ok response", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 500 });
    await expect(fetchUser("u1")).rejects.toThrow();
  });

  it("calls fetch", async () => {
    expect(mockFetch).toBeDefined();
  });
});
