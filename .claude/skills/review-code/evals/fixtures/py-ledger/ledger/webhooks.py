from .ledger import Ledger


def handle_payment_event(event: dict, ledger: Ledger) -> None:
    """Credit an account for a succeeded payment.

    The provider delivers each event at least once and retries until it receives a 2xx,
    so a slow response from us is followed by a second delivery of the same event.
    """
    if event["type"] != "payment.succeeded":
        return
    ledger.post(account=event["account"], amount=event["amount_cents"], ref=event["invoice"])
