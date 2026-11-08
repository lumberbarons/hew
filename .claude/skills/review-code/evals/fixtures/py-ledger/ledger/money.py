"""Integer-cent money helpers. Every amount here is a whole number of cents."""

from decimal import ROUND_HALF_UP, Decimal


def split_evenly(total: int, parts: int) -> list[int]:
    """Split ``total`` cents into ``parts`` shares that sum to ``total``."""
    if parts < 1:
        raise ValueError("parts must be at least 1")
    return [total // parts] * parts


def scale(amount: int, rate: Decimal) -> int:
    """Multiply cents by a rate, rounding half up to a whole cent."""
    return int((Decimal(amount) * rate).quantize(Decimal(1), rounding=ROUND_HALF_UP))
