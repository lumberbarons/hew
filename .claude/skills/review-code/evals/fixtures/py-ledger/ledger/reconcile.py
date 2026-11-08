from .models import Entry, Invoice


def unmatched(entries: list[Entry], invoice_refs: list[str]) -> list[Entry]:
    """Entries whose ref is not a known invoice. Runs nightly over one day's data, so the
    quadratic scan is deliberate."""
    return [e for e in entries if not any(e.ref == ref for ref in invoice_refs)]
