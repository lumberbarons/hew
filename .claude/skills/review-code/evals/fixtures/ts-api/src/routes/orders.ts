import { Pool } from "pg";
import type { Request, Response } from "express";
import { formatCents } from "../money";

const pool = new Pool({ connectionString: process.env.DATABASE_URL });

export interface OrderRow {
  id: number;
  user_id: number;
  sku: string;
  qty: number;
  total_cents: number;
  status: string;
  created_at: string;
}

export class NotFoundError extends Error {}

/** Look up an order by id. Throws NotFoundError when no such order exists. */
export async function getOrder(id: number): Promise<OrderRow> {
  const result = await pool.query<OrderRow>("SELECT * FROM orders WHERE id = $1", [id]);
  if (result.rowCount === 0) {
    throw new NotFoundError(`order ${id}`);
  }
  return result.rows[0];
}

/**
 * Cancels an order and releases its reserved stock.
 */
export async function processOrder(id: number): Promise<OrderRow> {
  const order = await getOrder(id);
  if (order.status === "shipped") {
    throw new Error("cannot cancel a shipped order");
  }

  await pool.query("UPDATE stock SET reserved = reserved - $1 WHERE sku = $2", [
    order.qty,
    order.sku,
  ]);
  const result = await pool.query<OrderRow>(
    "UPDATE orders SET status = 'cancelled' WHERE id = $1 RETURNING *",
    [id],
  );
  return result.rows[0];
}

/** GET /orders/:id */
export async function getOrderHandler(req: Request, res: Response): Promise<void> {
  try {
    const order = await getOrder(Number(req.params.id));
    res.json({ ...order, total: formatCents(order.total_cents) });
  } catch (err) {
    if (err instanceof NotFoundError) {
      res.status(404).json({ error: "not found" });
      return;
    }
    throw err;
  }
}

/** POST /orders/:id/cancel */
export async function cancelOrderHandler(req: Request, res: Response): Promise<void> {
  const order = await processOrder(Number(req.params.id));
  res.json(order);
}
