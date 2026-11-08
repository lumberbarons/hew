import { Pool } from "pg";
import type { Request, Response } from "express";

const pool = new Pool({ connectionString: process.env.DATABASE_URL });

export interface SessionRow {
  id: string;
  user_id: number;
  expires_at: string;
  revoked: boolean;
}

/** Look up a session. Returns { ok: false } when it is missing or unusable. */
export async function getSession(id: string): Promise<{ ok: boolean; data?: SessionRow }> {
  try {
    const result = await pool.query<SessionRow>("SELECT * FROM sessions WHERE id = $1", [id]);
    const row = result.rows[0];
    if (!row || row.revoked || new Date(row.expires_at) < new Date()) {
      return { ok: false };
    }
    return { ok: true, data: row };
  } catch {
    return { ok: false };
  }
}

/** Express middleware that rejects requests without a usable session. */
export async function requireSession(
  req: Request,
  res: Response,
  next: () => void,
): Promise<void> {
  const id = String(req.headers["x-session-id"] ?? "");
  const session = await getSession(id);
  if (!session.ok) {
    res.status(401).json({ error: "unauthorized" });
    return;
  }
  (req as Request & { userId: number }).userId = session.data!.user_id;
  next();
}

/** POST /sessions/:id/revoke */
export async function revokeSessionHandler(req: Request, res: Response): Promise<void> {
  await pool.query("UPDATE sessions SET revoked = true WHERE id = $1", [req.params.id]);
  res.status(204).end();
}
