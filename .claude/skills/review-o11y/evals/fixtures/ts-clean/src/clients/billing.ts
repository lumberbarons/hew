import { log } from "../context";

const MAX_ATTEMPTS = 3;

export class BillingUnavailableError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options);
    this.name = "BillingUnavailableError";
  }
}

/**
 * Charges the billing provider. Retries transient failures, and reports each
 * retry so a flaking provider is visible before the budget is exhausted.
 */
export async function charge(
  customerId: string,
  amountCents: number,
): Promise<{ chargeId: string }> {
  let lastError: unknown;

  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const start = Date.now();
    try {
      const result = await post("/charges", { customerId, amountCents });
      log().debug(
        { customerId, amountCents, attempt, durationMs: Date.now() - start },
        "billing charge succeeded",
      );
      return result;
    } catch (err) {
      lastError = err;
      if (attempt < MAX_ATTEMPTS) {
        log().warn(
          { customerId, attempt, maxAttempts: MAX_ATTEMPTS, err },
          "billing charge failed, retrying",
        );
      }
    }
  }

  throw new BillingUnavailableError(
    `failed to charge customer ${customerId} after ${MAX_ATTEMPTS} attempts`,
    { cause: lastError },
  );
}

async function post(path: string, body: unknown): Promise<any> {
  return { chargeId: "ch_1" };
}
