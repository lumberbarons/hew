"""Tables and indexes for a database that does not exist yet."""


def init_db(conn):
    conn.execute(
        """CREATE TABLE IF NOT EXISTS orders (
            id INTEGER PRIMARY KEY,
            customer TEXT NOT NULL,
            total REAL NOT NULL,
            created_at TEXT NOT NULL
        )"""
    )
    conn.execute("CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders(customer)")
    conn.execute(
        """CREATE TABLE IF NOT EXISTS audit_log (
            id INTEGER PRIMARY KEY,
            order_id INTEGER NOT NULL,
            event TEXT NOT NULL,
            at TEXT NOT NULL
        )"""
    )
    conn.commit()
