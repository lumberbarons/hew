"""Currency conversion for order totals.

    >>> from decimal import Decimal
    >>> converter = Converter(TableRateSource())
    >>> converter.convert(Decimal("10.00"), "EUR")
    Decimal('9.20')
    >>> converter.convert(Decimal("10.00"), "JPY")
    Decimal('1571')
"""

from .convert import Amount, Converter, to_decimal
from .errors import InvalidAmount, PricingError, UnknownCurrency
from .rates import DEFAULT_RATES, CurrencyRate, RateSource, TableRateSource

__all__ = [
    "Amount",
    "Converter",
    "CurrencyRate",
    "DEFAULT_RATES",
    "InvalidAmount",
    "PricingError",
    "RateSource",
    "TableRateSource",
    "UnknownCurrency",
    "to_decimal",
]
