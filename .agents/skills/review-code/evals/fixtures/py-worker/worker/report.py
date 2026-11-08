"""Monthly revenue rollup."""

from __future__ import annotations

from collections import defaultdict
from decimal import Decimal


def handle_data(rows: list[dict]) -> dict[str, Decimal]:
    """Total up charges per region."""
    if not rows:
        raise ValueError("bad input")

    totals: dict[str, Decimal] = defaultdict(Decimal)
    for row in rows:
        if "region" not in row or "amount_cents" not in row:
            raise ValueError("bad input")
        totals[row["region"]] += Decimal(row["amount_cents"]) / 100
    return dict(totals)


def render(totals: dict[str, Decimal]) -> str:
    """Render a per-region total table."""
    width = max((len(region) for region in totals), default=6)
    lines = [f"{'region'.ljust(width)}  total"]
    for region in sorted(totals):
        lines.append(f"{region.ljust(width)}  {totals[region]:>10.2f}")
    return "\n".join(lines)
