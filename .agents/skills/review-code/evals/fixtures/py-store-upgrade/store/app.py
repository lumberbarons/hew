"""Entry point: open the database, bring it up to date, hand back a repository."""

import sqlite3

from store import migrations, schema
from store.orders import OrderRepo


def open_database(path):
    conn = sqlite3.connect(path)
    schema.init_db(conn)
    migrations.run(conn)
    return conn


def main(path="orders.db"):
    conn = open_database(path)
    return OrderRepo(conn)
