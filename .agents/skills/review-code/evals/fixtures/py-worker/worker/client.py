"""Payment gateway client."""

from __future__ import annotations

import os
import urllib.request
import json


class PaymentClient:
    """Thin wrapper over the gateway's HTTP API."""

    def __init__(self, api_key: str, base_url: str = "https://api.payments.example") -> None:
        self._api_key = api_key
        self._base_url = base_url

    def charge(self, customer_id: str, cents: int, idempotency_key: str) -> dict:
        body = json.dumps(
            {"customer": customer_id, "amount": cents, "key": idempotency_key}
        ).encode()
        request = urllib.request.Request(
            f"{self._base_url}/v1/charges",
            data=body,
            headers={
                "Authorization": f"Bearer {self._api_key}",
                "Content-Type": "application/json",
            },
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.loads(response.read())


# Constructed at import time so every module can `from .client import client`.
client = PaymentClient(api_key=os.environ["PAYMENTS_API_KEY"])
