from .models import Entry


class Ledger:
    """Append-only entries per account. ``store`` provides append() and for_account()."""

    def __init__(self, store):
        self._store = store

    def post(self, account: str, amount: int, ref: str) -> None:
        self._store.append(Entry(seq=self._store.next_seq(), account=account, amount=amount, ref=ref))

    def entries_for(self, account: str) -> list[Entry]:
        """Newest first."""
        return sorted(self._store.for_account(account), key=lambda e: e.seq, reverse=True)
