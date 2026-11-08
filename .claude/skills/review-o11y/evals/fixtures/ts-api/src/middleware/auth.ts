import { Request, Response, NextFunction } from "express";
import winston from "winston";

const logger = winston.createLogger({
  level: "info",
  format: winston.format.json(),
  transports: [new winston.transports.Console()],
});

export function authenticate(req: Request, res: Response, next: NextFunction) {
  const authHeader = req.headers["authorization"];
  console.log(`[auth] incoming request with auth header: ${authHeader}`);

  if (!authHeader) {
    logger.error("Missing authorization header.");
    return res.status(401).json({ error: "unauthorized" });
  }

  const token = authHeader.replace(/^Bearer\s+/, "");
  logger.info("validating token", { token });

  try {
    const payload = verifyToken(token);
    (req as any).user = payload;
    next();
  } catch (err) {
    logger.error(err);
    throw err;
  }
}

function verifyToken(token: string): { sub: string } {
  if (!token) throw new Error("empty");
  return { sub: "fake" };
}
