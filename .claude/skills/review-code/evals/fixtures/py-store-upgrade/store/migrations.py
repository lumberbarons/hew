"""Changes applied on top of the schema that schema.init_db creates."""


def run(conn):
    # Orders now carry whole cents instead of fractional currency units.
    conn.execute("ALTER TABLE orders ADD COLUMN total_cents INTEGER")
    conn.execute("UPDATE orders SET total_cents = CAST(ROUND(total * 100) AS INTEGER)")
    # Lookups are by customer, newest first.
    conn.execute(
        "CREATE INDEX idx_orders_customer_created ON orders(customer, created_at)"
    )
    conn.commit()
