"""Converting USD amounts into other currencies."""

from __future__ import annotations

from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from typing import Sequence

from .errors import InvalidAmount
from .rates import CurrencyRate, RateSource

Amount = Decimal | int | str

# Converted amounts are quantized, and quantize fails once a result needs more
# digits than the decimal context allows. Rejecting oversized inputs up front
# turns that into an InvalidAmount with a message naming the limit.
MAX_AMOUNT = Decimal("1e15")


def to_decimal(amount: Amount) -> Decimal:
    """Coerce ``amount`` to ``Decimal``, raising ``InvalidAmount`` if it cannot be.

    ``float`` is deliberately not accepted: ``Decimal(0.1)`` captures the binary
    approximation rather than the value the caller wrote, so money arriving as a
    float is a mistake worth surfacing rather than silently rounding away.
    """
    if isinstance(amount, bool):
        raise InvalidAmount(amount, "a boolean is not an amount")

    if isinstance(amount, Decimal):
        candidate = amount
    elif isinstance(amount, (int, str)):
        try:
            candidate = Decimal(amount)
        except InvalidOperation:
            raise InvalidAmount(amount, "not a decimal number") from None
    else:
        raise InvalidAmount(amount, f"unsupported type {type(amount).__name__}")

    if not candidate.is_finite():
        raise InvalidAmount(amount, "not a finite number")
    if candidate < 0:
        raise InvalidAmount(amount, "must not be negative")
    if candidate > MAX_AMOUNT:
        raise InvalidAmount(amount, f"exceeds the maximum priceable amount {MAX_AMOUNT}")
    return candidate


def _round_to_places(amount: Decimal, places: int) -> Decimal:
    """Round ``amount`` to ``places`` decimal places, half away from zero."""
    return amount.quantize(Decimal(1).scaleb(-places), rounding=ROUND_HALF_UP)


def _priced(amount: Amount, rate: CurrencyRate) -> Decimal:
    """Convert one USD amount at ``rate``, rounded to that currency's precision.

    Bounding the input is not enough to bound the product: rates come from an
    injected source, so a large enough one can still push the result past the
    decimal context. Catching that here keeps the promise that everything this
    package raises is a ``PricingError``.
    """
    try:
        return _round_to_places(to_decimal(amount) * rate.rate, rate.places)
    except InvalidOperation:
        raise InvalidAmount(
            amount, f"converted value is too large to represent at {rate.places} decimal places"
        ) from None


class Converter:
    """Converts USD amounts using rates from an injected source."""

    def __init__(self, rates: RateSource) -> None:
        self._rates = rates

    def convert(self, amount: Amount, to: str) -> Decimal:
        """Convert a USD ``amount`` into the ``to`` currency."""
        return _priced(amount, self._rates.rate_for(to))

    def convert_all(self, amounts: Sequence[Amount], to: str) -> list[Decimal]:
        """Convert several USD amounts into the same currency.

        Looks the rate up once. A bad amount raises ``InvalidAmount`` carrying
        its position in ``amounts``.
        """
        rate = self._rates.rate_for(to)
        converted = []
        for index, amount in enumerate(amounts):
            try:
                converted.append(_priced(amount, rate))
            except InvalidAmount as err:
                raise InvalidAmount(err.amount, err.reason, index=index) from None
        return converted
