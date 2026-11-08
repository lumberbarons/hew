"""Currency conversion helpers. All money is integer cents."""

import math


class RateError(ValueError):
    """Raised when a rate string cannot be parsed or is out of range."""


def parse_rate(text: str) -> float:
    """Parse a rate like '1.2345'. Rates must be finite, positive and under 1000."""
    try:
        rate = float(text)
    except (TypeError, ValueError) as exc:
        raise RateError(f"unparseable rate: {text!r}") from exc
    if not math.isfinite(rate):
        raise RateError(f"rate must be finite, got {text!r}")
    if rate <= 0:
        raise RateError(f"rate must be positive, got {rate}")
    if rate >= 1000:
        raise RateError(f"rate out of range: {rate}")
    return rate


def convert(amount_cents: int, rate: float) -> int:
    """Convert cents at the given rate, rounding halves up. Both inputs must be positive."""
    if amount_cents < 0:
        raise ValueError("amount_cents must not be negative")
    if rate <= 0:
        raise ValueError("rate must be positive")
    return _round_half_up(amount_cents * rate)


def _round_half_up(value: float) -> int:
    return int(value + 0.5)
