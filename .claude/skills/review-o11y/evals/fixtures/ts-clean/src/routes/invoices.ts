import { Request, Response } from "express";
import { log } from "../context";
import { charge } from "../clients/billing";
import { toCents, applyTaxCents } from "../lib/money";

export async function createInvoice(req: Request, res: Response) {
  const { customerId, amount, taxRate } = req.body;

  if (!customerId || !amount) {
    log().info(
      { customerId, hasAmount: Boolean(amount) },
      "invoice request rejected: customerId and amount are required",
    );
    return res.status(400).json({ error: "customerId and amount are required" });
  }

  let totalCents: number;
  try {
    totalCents = applyTaxCents(toCents(amount), taxRate ?? 0);
  } catch (err) {
    log().info({ customerId, err }, "invoice request rejected: invalid amount or tax rate");
    return res.status(400).json({ error: "invalid amount or tax rate" });
  }

  // Held outside the try so a failure after the charge still reports the id
  // needed to reconcile or refund it.
  let chargeId: string | undefined;
  try {
    ({ chargeId } = await charge(customerId, totalCents));
    const invoice = await insertInvoice(customerId, totalCents, chargeId);
    log().info({ customerId, invoiceId: invoice.id, totalCents, chargeId }, "invoice created");
    return res.status(201).json(invoice);
  } catch (err) {
    log().error({ customerId, totalCents, chargeId, err }, "failed to create invoice");
    return res.status(502).json({ error: "billing unavailable" });
  }
}

export async function getInvoice(req: Request, res: Response) {
  const invoiceId = req.params.id;

  const invoice = await loadInvoice(invoiceId);
  if (!invoice) {
    log().info({ invoiceId }, "invoice not found");
    return res.status(404).json({ error: "not found" });
  }

  return res.json(invoice);
}

async function insertInvoice(
  customerId: string,
  totalCents: number,
  chargeId: string,
): Promise<{ id: string }> {
  try {
    return await db.insert({ customerId, totalCents, chargeId });
  } catch (err) {
    throw new Error(`failed to insert invoice for customer ${customerId}`, { cause: err });
  }
}

async function loadInvoice(id: string): Promise<any> {
  try {
    return await db.selectOne(id);
  } catch (err) {
    throw new Error(`failed to load invoice ${id}`, { cause: err });
  }
}

declare const db: {
  insert(row: Record<string, unknown>): Promise<{ id: string }>;
  selectOne(id: string): Promise<any>;
};
