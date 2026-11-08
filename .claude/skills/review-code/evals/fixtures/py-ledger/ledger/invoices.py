from decimal import Decimal

from .models import Invoice, Line, Tier
from .money import scale, split_evenly

TIER_PERCENT = {Tier.STANDARD: 0, Tier.GOLD: 10}


def tier_percent(tier: Tier) -> int:
    return TIER_PERCENT[tier]


def build_invoice(lines: list[Line], tier: Tier, tax_rate: Decimal, payers: int = 1) -> Invoice:
    """Price ``lines`` for ``tier`` and divide the total between ``payers``."""
    subtotal = sum(line.unit_cents * line.qty for line in lines)
    discounted = subtotal - scale(subtotal, Decimal(tier_percent(tier)) / 100)
    total = discounted + scale(discounted, tax_rate)
    return Invoice(total_cents=total, shares=split_evenly(total, payers))
