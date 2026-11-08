from .ledger import Ledger
from .models import Entry


def page_of(entries: list[Entry], page: int, per_page: int = 20) -> list[Entry]:
    """The entries on 1-indexed ``page``; page 1 holds the newest ``per_page`` entries."""
    start = page * per_page
    return entries[start:start + per_page]


def statement_view(ledger: Ledger, account: str, page: int = 1) -> dict:
    entries = ledger.entries_for(account)
    return {"balance": sum(e.amount for e in entries), "entries": page_of(entries, page)}
