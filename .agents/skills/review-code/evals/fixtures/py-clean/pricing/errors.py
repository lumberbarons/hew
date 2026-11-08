"""Errors this package raises deliberately."""

from __future__ import annotations


class PricingError(Exception):
    """Common base class, so a caller can catch this package's errors as a group."""


class UnknownCurrency(PricingError):
    """Raised when no rate is configured for a currency."""

    def __init__(self, code: str, available: tuple[str, ...]) -> None:
        self.code = code
        self.available = available
        listed = ", ".join(sorted(available)) or "none"
        super().__init__(f"no rate configured for {code!r}; configured currencies are {listed}")


class InvalidAmount(PricingError):
    """Raised when an amount cannot be priced.

    ``index`` is set when the amount came from a batch, so a caller can map the
    failure back to the row it came from.
    """

    def __init__(self, amount: object, reason: str, *, index: int | None = None) -> None:
        self.amount = amount
        self.reason = reason
        self.index = index
        position = "" if index is None else f" at index {index}"
        super().__init__(f"cannot price amount {amount!r}{position}: {reason}")
