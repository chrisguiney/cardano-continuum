"""units.py: which addresses count as one user."""

import units
from guide_lib import fake_address


def test_unit_is_the_stake_credential_when_there_is_one():
    """Two base addresses with different payment keys and the same stake key
    are one unit. A script stake credential is a different unit from a key
    with the same bytes. Addresses without a stake part (enterprise, Byron)
    and stake=False keep the address as the unit."""
    payment_1, payment_2, stake = bytes([1]) * 28, bytes([2]) * 28, bytes([9]) * 28
    first = fake_address("addr_test", bytes([0x00]) + payment_1 + stake)
    second = fake_address("addr_test", bytes([0x00]) + payment_2 + stake)
    script_stake = fake_address("addr_test", bytes([0x20]) + payment_1 + stake)
    enterprise = fake_address("addr_test", bytes([0x60]) + payment_1)
    byron = "Ae2tdPwUPEZ5zk"
    assert units.unit_key(first) == units.unit_key(second) == "k:" + stake.hex()
    assert units.unit_key(script_stake) == "s:" + stake.hex()
    assert units.unit_key(enterprise) == enterprise
    assert units.unit_key(byron, stake=True) == byron
    assert units.unit_key(first, stake=False) == first
