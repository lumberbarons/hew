"""Customer notification on invoice events."""


class Notifier:
    def __init__(self, transport):
        self.transport = transport

    def invoice_issued(self, invoice) -> bool:
        return self.transport.send(
            to=invoice.customer,
            subject=f"Invoice for {invoice.total_cents} cents",
            body=f"status={invoice.status} tax={invoice.tax_cents}",
        )

    def invoice_refunded(self, invoice, amount_cents: int) -> bool:
        return self.transport.send(
            to=invoice.customer,
            subject="Refund processed",
            body=f"refunded={amount_cents} remaining={invoice.total_cents}",
        )
