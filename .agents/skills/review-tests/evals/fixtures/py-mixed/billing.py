"""Invoice assembly. All money is integer cents."""

from dataclasses import dataclass, field


@dataclass
class Line:
    sku: str
    qty: int
    unit_cents: int


@dataclass
class Invoice:
    customer: str
    lines: list[Line] = field(default_factory=list)
    subtotal_cents: int = 0
    tax_cents: int = 0
    total_cents: int = 0
    status: str = "draft"


def build_invoice(customer: str, lines: list[Line], tax_rate: float) -> Invoice:
    """Assemble an invoice: subtotal, tax, total, and issued status."""
    if not lines:
        raise ValueError("an invoice needs at least one line")
    subtotal = sum(_line_total(line) for line in lines)
    tax = apply_tax(subtotal, tax_rate)
    return Invoice(
        customer=customer,
        lines=lines,
        subtotal_cents=subtotal,
        tax_cents=tax,
        total_cents=subtotal + tax,
        status="issued",
    )


def apply_tax(subtotal_cents: int, rate: float) -> int:
    """Tax, rounded down. Rates above 1.0 are rejected."""
    if rate < 0 or rate > 1:
        raise ValueError(f"tax rate out of range: {rate}")
    return int(subtotal_cents * rate)


def refund(invoice: Invoice, amount_cents: int) -> Invoice:
    """Refund against an issued invoice, moving it to refunded or partially-refunded."""
    if invoice.status != "issued":
        raise ValueError("only issued invoices can be refunded")
    if amount_cents > invoice.total_cents:
        raise ValueError("refund exceeds invoice total")
    invoice.total_cents -= amount_cents
    invoice.status = "refunded" if invoice.total_cents == 0 else "partially-refunded"
    return invoice


def _line_total(line: Line) -> int:
    if line.qty < 0:
        raise ValueError("negative quantity")
    return line.qty * line.unit_cents
