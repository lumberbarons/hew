const BASE = process.env.API_BASE ?? "https://api.example.com";

export interface User {
  id: string;
  email: string;
  plan: "free" | "pro";
}

export async function fetchUser(id: string): Promise<User> {
  const res = await fetch(`${BASE}/users/${id}`);
  if (!res.ok) throw new Error(`fetchUser failed: ${res.status}`);
  return (await res.json()) as User;
}

export async function postOrder(userId: string, sku: string, qty: number) {
  const res = await fetch(`${BASE}/orders`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ userId, sku, qty, status: "ok" }),
  });
  if (!res.ok) throw new Error(`postOrder failed: ${res.status}`);
  return (await res.json()) as { orderId: string; total: number };
}

export async function endSession(token: string): Promise<boolean> {
  const res = await fetch(`${BASE}/sessions/${token}`, { method: "DELETE" });
  return res.status === 204;
}

// Refreshes an expiring session. No test imports this.
export async function refreshSession(token: string): Promise<string> {
  const res = await fetch(`${BASE}/sessions/${token}/refresh`, { method: "POST" });
  if (!res.ok) throw new Error("refresh failed");
  const body = (await res.json()) as { token: string };
  return body.token;
}
