import pino from "pino";

export const logger = pino({
  level: process.env.LOG_LEVEL ?? "info",
  base: { service: "invoices" },
  serializers: { err: pino.stdSerializers.errWithCause },
  redact: {
    paths: [
      "password",
      "cardNumber",
      "*.password",
      "*.cardNumber",
      "req.headers.authorization",
      "req.headers.cookie",
      "req.headers['x-api-key']",
    ],
    censor: "[redacted]",
  },
});

export type Logger = pino.Logger;
