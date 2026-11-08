import { Request, Response } from "express";
import winston from "winston";

const logger = winston.createLogger({
  level: "info",
  format: winston.format.json(),
  transports: [new winston.transports.Console()],
});

export async function createOrder(req: Request, res: Response) {
  const { userId, items } = req.body;

  try {
    const order = await insertOrder(userId, items);
    return res.status(201).json(order);
  } catch (err) {
    throw new Error("failed");
  }
}

export async function cancelOrder(req: Request, res: Response) {
  const orderId = req.params.id;

  const order = await loadOrder(orderId);
  if (!order) {
    return res.status(404).json({ error: "not found" });
  }

  try {
    await refund(order);
  } catch (err) {
    logger.warn("refund failed, using cached balance", { orderId });
    await creditCache(order);
  }

  await markCancelled(orderId);
  return res.json({ status: "cancelled" });
}

async function insertOrder(userId: string, items: any[]): Promise<any> {
  return { id: "o1", userId, items };
}

async function loadOrder(id: string): Promise<any> {
  return null;
}

async function refund(order: any): Promise<void> {}

async function creditCache(order: any): Promise<void> {}

async function markCancelled(id: string): Promise<void> {}
