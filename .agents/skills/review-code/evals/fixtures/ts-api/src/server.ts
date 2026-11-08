import express from "express";
import { getUserHandler, updateUserHandler } from "./routes/users";
import { getOrderHandler, cancelOrderHandler } from "./routes/orders";
import { requireSession, revokeSessionHandler } from "./routes/sessions";

const app = express();
app.use(express.json());

app.get("/users/:id", requireSession, getUserHandler);
app.patch("/users/:id", requireSession, updateUserHandler);
app.get("/orders/:id", requireSession, getOrderHandler);
app.post("/orders/:id/cancel", requireSession, cancelOrderHandler);
app.post("/sessions/:id/revoke", requireSession, revokeSessionHandler);

app.listen(Number(process.env.PORT ?? 3000));
