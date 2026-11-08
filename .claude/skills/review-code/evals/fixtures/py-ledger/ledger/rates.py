from collections.abc import Callable
from decimal import Decimal


class FetchError(Exception):
    """The rate provider could not be reached or returned something unusable."""


class RateCache:
    """Currency -> rate to USD, refreshed from ``fetch`` once ``ttl`` seconds have passed.

    Callers price invoices from these rates and rely on every rate returned being no
    older than ``ttl`` seconds.
    """

    def __init__(self, fetch: Callable[[], dict[str, Decimal]], clock: Callable[[], float],
                 ttl: float = 3600.0):
        self._fetch = fetch
        self._clock = clock
        self._ttl = ttl
        self._rates: dict[str, Decimal] = {}
        self._loaded_at: float | None = None

    def rate(self, currency: str) -> Decimal:
        self._refresh()
        return self._rates[currency]

    def _refresh(self) -> None:
        now = self._clock()
        if self._loaded_at is not None and now - self._loaded_at < self._ttl:
            return
        try:
            self._rates = self._fetch()
        except FetchError:
            return
        self._loaded_at = now
