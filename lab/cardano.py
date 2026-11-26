"""Cardano units of measure used across the lab.

Chain tables count money in lovelace and time in slots; people think in ADA
and days. Every conversion goes through the constants here, so a number in
the code always says which unit it is in.

Guide: section 7 of guide.ipynb ("units") and its glossary.
"""

from __future__ import annotations

# 1 ADA = 1,000,000 lovelace. The ledger stores only whole lovelace, so
# on-chain amounts are integers and there is no rounding to worry about.
# https://en.wikipedia.org/wiki/Cardano_(blockchain_platform)
LOVELACE_PER_ADA = 1_000_000

# The "2 ADA pile". The ledger refuses an output that does not carry enough
# ADA to pay for the space it occupies (the min-UTxO rule, about 1 ADA for a
# plain output and more when it carries tokens), so wallets and dApps attach
# a round 2 ADA to token transfers. Real payment sizes therefore have a
# spike of mass on exactly this value instead of a smooth curve, and any
# fit of payment sizes has to survive thousands of identical values.
# Nothing is tuned to this number: it is here because the tests and plots
# refer to it by name.
# Rule and its parameter coinsPerUTxOByte: https://cips.cardano.org/cip/CIP-55
MIN_OUTPUT_PILE_LOVELACE = 2 * LOVELACE_PER_ADA

SECONDS_PER_DAY = 86_400

# A slot is the chain's clock tick: at most one block per slot. Since the
# Shelley era a slot lasts one second on mainnet, preprod and preview, so
# slot differences are seconds there. Byron-era slots lasted 20 s and a
# continuum devnet may use another length, which is why every function that
# turns slots into time takes the slot length instead of assuming it.
# The length is `slotLength` in a network's Shelley genesis file.
DEFAULT_SLOT_SECONDS = 1.0


def slots_in_days(days: float, slot_seconds: float) -> int:
    """How many whole slots fit in a number of days."""
    return int(days * SECONDS_PER_DAY / slot_seconds)


# Where each network's fixed-length epochs start: (first Shelley slot, its
# epoch number, slots per epoch). Byron epochs before it were shorter, so
# epochs are counted from the Shelley start. Pool retirements name the
# epoch they take effect in, so telling a retired pool from a live one
# needs this. Values are from each network's Shelley genesis (epochLength)
# and its Byron history: mainnet had 208 Byron epochs of 21,600 slots,
# preprod 4; preview started in Shelley-era rules at slot 0.
EPOCH_SCHEDULES = {
    "mainnet": (4_492_800, 208, 432_000),
    "preprod": (86_400, 4, 432_000),
    "preview": (0, 0, 86_400),
}


def epoch_of_slot(slot: int, network: str) -> int | None:
    """The epoch of a slot, or None for a network without a known schedule
    (a devnet), whose callers then treat every announced retirement as
    already in effect."""
    if network not in EPOCH_SCHEDULES:
        return None
    first_slot, first_epoch, length = EPOCH_SCHEDULES[network]
    return first_epoch + max(slot - first_slot, 0) // length
