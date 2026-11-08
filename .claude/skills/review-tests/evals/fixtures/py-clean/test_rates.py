import pytest

from rates import RateError, convert, parse_rate


@pytest.mark.parametrize(
    "text,expected",
    [("1", 1.0), ("0.5", 0.5), ("1.2345", 1.2345), ("999.99", 999.99)],
)
def test_parse_rate_accepts_valid_rates(text, expected):
    assert parse_rate(text) == expected


@pytest.mark.parametrize("text", ["", "abc", None, "1.2.3"])
def test_parse_rate_rejects_unparseable_input(text):
    with pytest.raises(RateError, match="unparseable rate"):
        parse_rate(text)


@pytest.mark.parametrize("text", ["nan", "NaN", "inf", "-inf"])
def test_parse_rate_rejects_non_finite_rates(text):
    with pytest.raises(RateError, match="must be finite"):
        parse_rate(text)


@pytest.mark.parametrize("text", ["0", "-1", "-0.001"])
def test_parse_rate_rejects_non_positive_rates(text):
    with pytest.raises(RateError, match="must be positive"):
        parse_rate(text)


def test_parse_rate_rejects_rate_at_upper_bound():
    with pytest.raises(RateError, match="out of range"):
        parse_rate("1000")


def test_parse_rate_accepts_rate_just_below_upper_bound():
    assert parse_rate("999.999") == 999.999


@pytest.mark.parametrize(
    "cents,rate,expected",
    [
        (0, 1.5, 0),
        (100, 1.0, 100),
        (100, 0.92, 92),
        (101, 0.5, 51),  # 50.5 rounds up, not to even
        (1, 0.4, 0),
    ],
)
def test_convert_rounds_halves_up(cents, rate, expected):
    assert convert(cents, rate) == expected


def test_convert_rejects_negative_amounts():
    with pytest.raises(ValueError, match="must not be negative"):
        convert(-1, 1.0)


@pytest.mark.parametrize("rate", [0.0, -0.5])
def test_convert_rejects_non_positive_rates(rate):
    with pytest.raises(ValueError, match="rate must be positive"):
        convert(100, rate)


def test_convert_uses_the_supplied_rate_not_a_default(known_rates):
    assert convert(1000, known_rates["EUR"]) == 920
    assert convert(1000, known_rates["GBP"]) == 790
