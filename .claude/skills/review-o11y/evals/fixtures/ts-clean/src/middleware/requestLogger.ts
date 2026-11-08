import { Request, Response, NextFunction } from "express";
import { randomUUID } from "node:crypto";
import { runWithRequestId, log } from "../context";

export function requestLogger(req: Request, res: Response, next: NextFunction) {
  const requestId = (req.headers["x-request-id"] as string) ?? randomUUID();
  res.setHeader("x-request-id", requestId);

  runWithRequestId(requestId, () => {
    const start = process.hrtime.bigint();
    let logged = false;

    // `finish` covers responses we complete; `close` catches clients that hang
    // up first. Those are the slowest requests, so losing them would bias every
    // latency figure computed from these lines.
    const complete = () => {
      if (logged) return;
      logged = true;
      log().info(
        {
          method: req.method,
          path: req.route?.path ?? req.path,
          status: res.statusCode,
          durationMs: Number(process.hrtime.bigint() - start) / 1e6,
          aborted: !res.writableEnded,
        },
        "request completed",
      );
    };

    res.on("finish", complete);
    res.on("close", complete);

    next();
  });
}
