"""Order persistence."""


class OrderRepo:
    def __init__(self, conn):
        self._conn = conn

    def add(self, customer, total_cents, created_at):
        cur = self._conn.execute(
            "INSERT INTO orders (customer, total_cents, created_at) VALUES (?, ?, ?)",
            (customer, total_cents, created_at),
        )
        self._conn.commit()
        return cur.lastrowid

    def for_customer(self, customer, limit=20):
        rows = self._conn.execute(
            "SELECT id, total_cents, status, created_at FROM orders "
            "WHERE customer = ? ORDER BY created_at DESC LIMIT ?",
            (customer, limit),
        ).fetchall()
        return [
            {"id": r[0], "total_cents": r[1], "status": r[2], "created_at": r[3]}
            for r in rows
        ]
