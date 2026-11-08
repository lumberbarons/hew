"""Nightly invoice batch."""

from __future__ import annotations

import csv
import logging
import smtplib
from email.message import EmailMessage
from pathlib import Path

from .billing import charge_customer

log = logging.getLogger(__name__)

SMTP_HOST = "smtp.internal.example"
FINANCE_INBOX = "finance@example.com"


def process_batch(csv_path: Path, report_path: Path) -> int:
    """Read the nightly invoice CSV, charge each customer, write a report and
    email it to finance. Returns the number of successful charges."""
    charged = 0
    failures: list[tuple[str, str]] = []
    lines: list[str] = []

    with csv_path.open(newline="") as handle:
        for row_number, row in enumerate(csv.DictReader(handle), start=2):
            customer = (row.get("customer_id") or "").strip()
            raw_amount = (row.get("amount_cents") or "").strip()

            if not customer:
                failures.append((str(row_number), "missing customer_id"))
                continue
            if not raw_amount:
                failures.append((customer, "missing amount_cents"))
                continue
            try:
                cents = int(raw_amount)
            except ValueError:
                failures.append((customer, f"amount_cents is not an integer: {raw_amount!r}"))
                continue
            if cents <= 0:
                failures.append((customer, f"amount_cents must be positive, got {cents}"))
                continue
            if cents > 5_000_00:
                if (row.get("approved_by") or "").strip() == "":
                    failures.append((customer, "charges over $5000 need approved_by"))
                    continue

            charge_id = charge_customer(customer, cents)
            if charge_id is None:
                failures.append((customer, "gateway did not accept the charge"))
                continue

            charged += 1
            lines.append(f"{customer},{cents},{charge_id}")

    report_path.write_text(
        "customer_id,amount_cents,charge_id\n"
        + "\n".join(lines)
        + "\n\n"
        + "\n".join(f"FAILED {who}: {why}" for who, why in failures)
        + "\n"
    )

    message = EmailMessage()
    message["Subject"] = f"Nightly billing: {charged} charged, {len(failures)} failed"
    message["From"] = "billing@example.com"
    message["To"] = FINANCE_INBOX
    message.set_content(report_path.read_text())
    with smtplib.SMTP(SMTP_HOST) as smtp:
        smtp.send_message(message)

    log.info("batch complete: %d charged, %d failed", charged, len(failures))
    return charged
