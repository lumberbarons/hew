import express from "express";
import { authenticate } from "./middleware/auth";
import { createUser, getUser } from "./routes/users";
import { createOrder, cancelOrder } from "./routes/orders";

const app = express();
app.use(express.json());
app.use(authenticate);

app.post("/users", createUser);
app.get("/users/:id", getUser);
app.post("/orders", createOrder);
app.post("/orders/:id/cancel", cancelOrder);

const port = Number(process.env.PORT ?? 3000);
app.listen(port, () => {
  console.log(`server listening on ${port}`);
});
