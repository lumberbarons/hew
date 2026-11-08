from types import MappingProxyType

import pytest

# Read-only reference data. MappingProxyType makes that enforceable, not just conventional.
KNOWN_RATES = MappingProxyType({"USD": 1.0, "EUR": 0.92, "GBP": 0.79})


@pytest.fixture(scope="session")
def known_rates():
    """Immutable lookup table shared across the suite — writes raise TypeError."""
    return KNOWN_RATES
