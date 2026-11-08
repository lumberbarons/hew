import { Pool } from "pg";
import type { Request, Response } from "express";

const pool = new Pool({ connectionString: process.env.DATABASE_URL });

export interface UserRow {
  id: number;
  email: string;
  display_name: string;
  password_hash: string;
  reset_token: string | null;
  created_at: string;
}

/** Look up a user by id. Returns null when no such user exists. */
export async function getUser(id: number): Promise<UserRow | null> {
  const result = await pool.query<UserRow>("SELECT * FROM users WHERE id = $1", [id]);
  return result.rows[0] ?? null;
}

/** GET /users/:id */
export async function getUserHandler(req: Request, res: Response): Promise<void> {
  const user = await getUser(Number(req.params.id));
  if (!user) {
    res.status(404).json({ error: "not found" });
    return;
  }
  res.json(user);
}

/** PATCH /users/:id */
export async function updateUserHandler(req: Request, res: Response): Promise<void> {
  const id = Number(req.params.id);
  const name = String(req.body.display_name ?? "").trim();
  if (name.length === 0) {
    res.status(400).json({ error: "display_name is required" });
    return;
  }

  const result = await pool.query<UserRow>(
    "UPDATE users SET display_name = $1 WHERE id = $2 RETURNING *",
    [name, id],
  );
  if (result.rowCount === 0) {
    res.status(404).json({ error: "not found" });
    return;
  }
  res.json(result.rows[0]);
}
