from billing import Invoice
from notify import Notifier


class FakeTransport:
    def __init__(self):
        self.sent = []
        self.last_subject = None
        self.last_body = None

    def send(self, to, subject, body):
        self.sent.append(to)
        self.last_subject = subject
        self.last_body = body
        return True


def test_invoice_issued_sends_a_notification():
    transport = FakeTransport()
    notifier = Notifier(transport)
    inv = Invoice(customer="acme", subtotal_cents=1000, tax_cents=100, total_cents=1100)

    result = notifier.invoice_issued(inv)

    assert result is True
    assert len(transport.sent) == 1


def test_invoice_refunded_sends_a_notification():
    transport = FakeTransport()
    notifier = Notifier(transport)
    inv = Invoice(customer="acme", total_cents=0, status="refunded")

    assert notifier.invoice_refunded(inv, 1100) is True
