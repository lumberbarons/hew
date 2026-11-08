import { AsyncLocalStorage } from "node:async_hooks";
import { logger, Logger } from "./logger";

const store = new AsyncLocalStorage<{ requestId: string; log: Logger }>();

export function runWithRequestId<T>(requestId: string, fn: () => T): T {
  return store.run({ requestId, log: logger.child({ requestId }) }, fn);
}

/** Request-scoped logger; falls back to the root logger outside a request. */
export function log(): Logger {
  return store.getStore()?.log ?? logger;
}

export function currentRequestId(): string | undefined {
  return store.getStore()?.requestId;
}
