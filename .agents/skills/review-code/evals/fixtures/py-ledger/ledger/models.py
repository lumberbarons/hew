from dataclasses import dataclass
from enum import Enum


class Tier(Enum):
    STANDARD = "standard"
    GOLD = "gold"


@dataclass(frozen=True)
class Line:
    sku: str
    unit_cents: int
    qty: int


@dataclass(frozen=True)
class Invoice:
    total_cents: int
    shares: list[int]


@dataclass(frozen=True)
class Entry:
    seq: int
    account: str
    amount: int
    ref: str
