import express from "express";
import { logger } from "./logger";
import { requestLogger } from "./middleware/requestLogger";
import { route, errorHandler } from "./middleware/errorHandler";
import { createInvoice, getInvoice } from "./routes/invoices";

const config = {
  port: Number(process.env.PORT ?? 3000),
  billingUrl: process.env.BILLING_URL ?? "https://billing.internal",
  logLevel: process.env.LOG_LEVEL ?? "info",
};

const app = express();
app.use(express.json());
app.use(requestLogger);

app.post("/invoices", route(createInvoice));
app.get("/invoices/:id", route(getInvoice));

app.use(errorHandler);

app.listen(config.port, () => {
  logger.info(
    { port: config.port, billingUrl: config.billingUrl, logLevel: config.logLevel },
    "server started",
  );
});

process.on("SIGTERM", () => {
  logger.info("received SIGTERM, shutting down");
  process.exit(0);
});
