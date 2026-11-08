import { describe, it, expect, vi } from "vitest";
import { postOrder } from "../src/client";

// Installed once at import time, shared by every test in this file.
const mockFetch = vi.fn();
globalThis.fetch = mockFetch as unknown as typeof fetch;

describe("postOrder", () => {
  it("sends the order payload", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ orderId: "o1", total: 500 }),
    });

    await postOrder("u1", "widget", 2);

    const body = mockFetch.mock.calls[0][1].body as string;
    expect(body).toContain('"status":"ok"');
  });

  it("throws when the server rejects", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 422 });
    await expect(postOrder("u1", "widget", 2)).rejects.toThrow();
  });
});
