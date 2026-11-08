"""Where exchange rates come from.

Rates are supplied to the converter rather than fetched by it, so a caller can
back them with a config file, an HTTP service, or a literal table in a test
without the conversion logic changing.

A rate travels with the number of minor-unit digits its currency uses, because
rounding a converted amount is only correct if you know that number: EUR takes
two digits, JPY takes none.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from types import MappingProxyType
from typing import Mapping, Protocol

from .errors import UnknownCurrency


@dataclass(frozen=True)
class CurrencyRate:
    """A conversion rate, and how many minor-unit digits its currency uses."""

    rate: Decimal
    places: int


# A read-only view, so importing this module cannot let one caller mutate the
# table another caller reads.
DEFAULT_RATES: Mapping[str, CurrencyRate] = MappingProxyType(
    {
        "USD": CurrencyRate(Decimal("1"), places=2),
        "EUR": CurrencyRate(Decimal("0.92"), places=2),
        "GBP": CurrencyRate(Decimal("0.79"), places=2),
        "JPY": CurrencyRate(Decimal("157.1"), places=0),
    }
)


class RateSource(Protocol):
    """Supplies the rate to convert a USD amount into another currency."""

    def rate_for(self, code: str) -> CurrencyRate:
        """Return the rate for ``code``, or raise ``UnknownCurrency``."""
        ...


class TableRateSource:
    """A rate source backed by an in-memory table.

    Currency codes are canonically uppercase (ISO 4217), so lookups are
    normalised rather than requiring every caller to remember to do it.
    """

    def __init__(self, rates: Mapping[str, CurrencyRate] = DEFAULT_RATES) -> None:
        self._rates = {code.upper(): rate for code, rate in rates.items()}

    def rate_for(self, code: str) -> CurrencyRate:
        try:
            return self._rates[code.upper()]
        except KeyError:
            raise UnknownCurrency(code, tuple(self._rates)) from None
