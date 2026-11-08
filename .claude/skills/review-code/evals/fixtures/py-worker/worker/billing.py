"""Charging customers."""

from __future__ import annotations

import logging
import time
import uuid

from .client import client

log = logging.getLogger(__name__)

MAX_ATTEMPTS = 3


def charge_customer(customer_id: str, cents: int) -> str | None:
    """Charge a customer and return the gateway charge id.

    Returns ``None`` if the charge did not go through.
    """
    key = str(uuid.uuid4())
    for attempt in range(MAX_ATTEMPTS):
        try:
            result = client.charge(customer_id, cents, key)
            return result["id"]
        except Exception as exc:
            log.error("charge failed: %s", exc)
            if attempt < MAX_ATTEMPTS - 1:
                time.sleep(2**attempt)
    return None


def refund_customer(charge_id: str, cents: int) -> bool:
    """Refund part or all of a charge. Returns whether it succeeded."""
    try:
        client.charge(charge_id, -cents, str(uuid.uuid4()))
        return True
    except Exception as exc:
        log.error("refund failed: %s", exc)
        return False
