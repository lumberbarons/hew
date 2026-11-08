import { Request, Response } from "express";
import winston from "winston";

const logger = winston.createLogger({
  level: "info",
  format: winston.format.json(),
  transports: [new winston.transports.Console()],
});

export async function createUser(req: Request, res: Response) {
  const { email, password, name } = req.body;

  if (!email) {
    logger.error("failed to create user: email missing");
    return res.status(400).json({ error: "email required" });
  }

  try {
    const user = await saveUser({ email, password, name });
    logger.info("user created", { userId: user.id });
    return res.status(201).json(user);
  } catch (err) {
    logger.error("could not save user", { err });
    return res.status(500).json({ error: "internal" });
  }
}

export async function getUser(req: Request, res: Response) {
  const uid = req.params.id;
  const user = await loadUser(uid);
  if (!user) {
    logger.error("user not found", { user_id: uid });
    return res.status(404).json({ error: "not found" });
  }
  return res.json(user);
}

async function saveUser(u: any): Promise<{ id: string }> {
  return { id: "u1" };
}

async function loadUser(id: string): Promise<any> {
  return null;
}
