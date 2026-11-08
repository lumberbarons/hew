"""Changes to databases created by an earlier release.

A new database is created at the latest version by schema.init_db, so these
only ever run against databases that already exist.
"""

MIGRATIONS = [
    (1, "ALTER TABLE orders ADD COLUMN status TEXT NOT NULL DEFAULT 'new'"),
]
LATEST = MIGRATIONS[-1][0]


def run(conn):
    current = conn.execute("PRAGMA user_version").fetchone()[0]
    for version, statement in MIGRATIONS:
        if version > current:
            conn.execute(statement)
            conn.execute(f"PRAGMA user_version = {version}")
    conn.commit()
