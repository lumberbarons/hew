import { Request, Response, NextFunction, RequestHandler } from "express";
import { log } from "../context";

/** Forwards async rejections to the terminal error handler below. */
export function route(handler: RequestHandler): RequestHandler {
  return (req, res, next) => {
    Promise.resolve(handler(req, res, next)).catch(next);
  };
}

/** Terminal owner for anything a route threw: logged once, with correlation. */
export function errorHandler(
  err: unknown,
  req: Request,
  res: Response,
  _next: NextFunction,
) {
  log().error({ method: req.method, path: req.path, err }, "request failed");

  if (res.headersSent) {
    return res.end();
  }
  return res.status(500).json({ error: "internal error" });
}
