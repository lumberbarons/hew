import time

import pytest

from billing import Invoice, Line, apply_tax, build_invoice, refund

# Shared across every test in this module.
_cache = {}


def test_1():
    inv = build_invoice("acme", [Line("widget", 2, 500)], 0.1)
    assert inv is not None


def test_build_invoice_sets_subtotal():
    inv = build_invoice("acme", [Line("widget", 2, 500)], 0.1)
    _cache["last"] = inv
    assert inv.subtotal_cents == 1000


def test_build_invoice_uses_cached_invoice_customer():
    inv = _cache["last"]
    assert inv.customer == "acme"


def test_build_invoice_rejects_empty_lines():
    with pytest.raises(ValueError):
        build_invoice("acme", [], 0.1)


@pytest.mark.parametrize("rate", [0.0, 0.5, 1.0])
def test_apply_tax_accepts_valid_rates(rate):
    assert apply_tax(1000, rate) == int(1000 * rate)


@pytest.mark.skip(reason="flaky on CI")
def test_refund_marks_invoice_refunded():
    inv = build_invoice("acme", [Line("widget", 1, 100)], 0.0)
    out = refund(inv, 100)
    assert out.status == "refunded"


def test_slow_invoice_build():
    time.sleep(1)
    inv = build_invoice("acme", [Line("widget", 1, 100)], 0.0)
    assert inv.total_cents == 100
