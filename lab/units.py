"""Units: which addresses belong to one user.

A Cardano wallet does not have one address. It derives a fresh payment
address for most transactions, and its funds are spread over all of them.
Counted per address, one busy wallet looks like hundreds of users who each
transact once, which would make every behavioural statistic wrong.

What the addresses of one wallet share is the *stake credential*: the key
(or script) that delegates the wallet's ADA to a stake pool. A Shelley
"base" address is the pair (payment credential, stake credential), so two
addresses with the same stake credential are, for practical purposes, the
same owner.

A **unit** is therefore the stake credential of an address when it has one,
and the address itself otherwise. One unit is one simulated actor.

What could go wrong: an exchange or custodian holds many customers behind
one stake credential (one unit, many people), and a user with several
wallets is several units. Enterprise addresses (no stake part, common for
scripts and exchanges) and Byron addresses stay one unit per address.

Guide: section 7 of guide.ipynb ("What is one user?").
Address format: CIP-19, https://cips.cardano.org/cip/CIP-19
"""

from __future__ import annotations

from typing import Callable

# bech32 writes 5 bits per character with this alphabet (BIP-173).
# https://github.com/bitcoin/bips/blob/master/bip-0173.mediawiki
_BECH32_ALPHABET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
_BECH32_VALUE = {char: value for value, char in enumerate(_BECH32_ALPHABET)}
_BECH32_CHECKSUM_CHARS = 6

# CIP-19 layout of a Shelley base address: one header byte, a 28-byte payment
# credential, then a 28-byte stake credential.
_CREDENTIAL_BYTES = 28
_STAKE_START = 1 + _CREDENTIAL_BYTES
_STAKE_END = _STAKE_START + _CREDENTIAL_BYTES
# The header's high four bits are the address type. Types 0-3 are the base
# addresses, the only ones that carry a stake credential inline.
_LAST_BASE_ADDRESS_TYPE = 3
# In a base address this header bit says the stake credential is a script
# hash (types 2 and 3) instead of a key hash (types 0 and 1).
_STAKE_IS_SCRIPT_BIT = 0x20


def bech32_payload(address: str) -> bytes | None:
    """Decoded data bytes of a bech32 string, or None when it is not bech32.

    The checksum is not verified: addresses come from a chain that already
    validated them, and a Byron base58 address simply fails the alphabet test.
    """
    separator = address.rfind("1")
    if separator < 1 or address != address.lower():
        return None
    data = address[separator + 1 :]
    if len(data) <= _BECH32_CHECKSUM_CHARS or any(char not in _BECH32_VALUE for char in data):
        return None
    return _pack_5bit_groups(data[:-_BECH32_CHECKSUM_CHARS])


def _pack_5bit_groups(chars: str) -> bytes:
    """Regroup 5-bit bech32 characters into bytes, dropping leftover pad bits."""
    buffer = bits_in_buffer = 0
    out = bytearray()
    for char in chars:
        buffer = (buffer << 5) | _BECH32_VALUE[char]
        bits_in_buffer += 5
        while bits_in_buffer >= 8:
            bits_in_buffer -= 8
            out.append((buffer >> bits_in_buffer) & 0xFF)
    return bytes(out)


def unit_key(address: str, stake: bool = True) -> str:
    """The unit of one address.

    A Shelley base address takes its stake credential, written as
    `k:<hex>` for a key hash and `s:<hex>` for a script hash so the two
    kinds never collide. Everything else (enterprise, pointer, Byron, or
    stake=False) is its own unit and returns the address unchanged.
    """
    if not stake:
        return address
    raw = bech32_payload(address)
    if raw is None or len(raw) < _STAKE_END:
        return address
    header = raw[0]
    if header >> 4 > _LAST_BASE_ADDRESS_TYPE:
        return address
    kind = "s" if header & _STAKE_IS_SCRIPT_BIT else "k"
    return f"{kind}:{raw[_STAKE_START:_STAKE_END].hex()}"


def unit_of(stake: bool = True) -> Callable[[list[str]], list[str]]:
    """A function mapping a list of addresses to their units.

    chain_stats.load_window takes it, so the choice between "one unit per
    stake credential" (stake=True) and "one unit per address" is made once.
    """
    return lambda addresses: [unit_key(address, stake) for address in addresses]
