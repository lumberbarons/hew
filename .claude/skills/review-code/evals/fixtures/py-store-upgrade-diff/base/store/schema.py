"""Tables and indexes for a database that does not exist yet."""

from store import migrations


def init_db(conn):
    exists = conn.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'orders'"
    ).fetchone()
    if exists:
        return
    conn.execute(
        """CREATE TABLE orders (
            id INTEGER PRIMARY KEY,
            customer TEXT NOT NULL,
            total REAL NOT NULL,
            status TEXT NOT NULL DEFAULT 'new',
            created_at TEXT NOT NULL
        )"""
    )
    conn.execute("CREATE INDEX idx_orders_customer ON orders(customer)")
    conn.execute(f"PRAGMA user_version = {migrations.LATEST}")
    conn.commit()
