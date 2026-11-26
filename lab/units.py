"""Units and per-unit features.

A unit is what an actor stands for: the stake credential of an address where
it has one (a wallet spreads its funds over many payment addresses that share
one), and the address itself otherwise. `unit_of` maps addresses to units;
`features` reads one row per unit from a connection built by common.prepare.
"""

from __future__ import annotations

import duckdb
import pandas as pd

_CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
_REV = {c: i for i, c in enumerate(_CHARSET)}


def bech32_payload(addr: str) -> bytes | None:
    """Decoded data bytes of a bech32 string, or None when it is not bech32.

    The checksum is not verified: addresses come from a chain that already
    validated them, and a Byron base58 address simply fails the charset test.
    """
    sep = addr.rfind("1")
    if sep < 1 or addr != addr.lower():
        return None
    data = addr[sep + 1 :]
    if len(data) < 7 or any(c not in _REV for c in data):
        return None
    acc = bits = 0
    out = bytearray()
    for c in data[:-6]:
        acc = (acc << 5) | _REV[c]
        bits += 5
        while bits >= 8:
            bits -= 8
            out.append((acc >> bits) & 0xFF)
    return bytes(out)


def unit_key(addr: str, stake: bool = True) -> str:
    """The unit of one address.

    Shelley base addresses (header types 0-3: payment credential then stake
    credential) take the stake credential, prefixed by its kind so a key hash
    and a script hash never collide. Everything else (enterprise, pointer,
    Byron, or stake=False) is its own unit.
    """
    if not stake:
        return addr
    raw = bech32_payload(addr)
    if raw is None or len(raw) < 57 or raw[0] >> 4 > 3:
        return addr
    kind = "s" if raw[0] & 0x20 else "k"
    return f"{kind}:{raw[29:57].hex()}"


def unit_of(stake: bool = True):
    """A function mapping addresses to units, for common.prepare."""
    return lambda addrs: [unit_key(a, stake) for a in addrs]


# One row per unit that sent at least one transaction in the window. Gaps and
# dormancy are in slots; a unit with a single transaction has no gap, so its
# median gap is the window and its longest dormancy is zero. Active days are
# days of slot time, so the slot length converts slots to seconds first.
FEATURE_SQL = """
WITH f AS (SELECT unit, hash, slot, block_index, n_in, n_out, kind
           FROM c_base WHERE unit IS NOT NULL),
g AS (SELECT unit, slot - lag(slot) OVER (PARTITION BY unit ORDER BY slot, block_index) AS gap FROM f),
gs AS (SELECT unit, median(gap) AS med_gap, max(gap) AS max_gap FROM g WHERE gap IS NOT NULL GROUP BY unit),
b AS (SELECT unit, count(*) AS n_tx, count(DISTINCT floor(slot * {slot_seconds} / 86400)) AS active_days,
             avg(n_in) AS in_per_tx, avg(n_out) AS out_per_tx,
             avg((kind = 'script')::int) AS script_share, avg((kind = 'stake')::int) AS stake_share
      FROM f GROUP BY unit),
v AS (SELECT f.unit, median(o.lovelace) FILTER (WHERE o.ounit <> f.unit) AS med_pay,
             median(o.lovelace) AS med_all
      FROM f JOIN c_outs o ON o.hash = f.hash WHERE o.lovelace > 0 GROUP BY f.unit),
r AS (SELECT f.unit, avg(o.reuse::int) AS reuse FROM f JOIN c_outs o ON o.hash = f.hash GROUP BY f.unit)
SELECT b.unit, b.n_tx, b.active_days, b.in_per_tx, b.out_per_tx, b.script_share, b.stake_share,
       coalesce(gs.med_gap, {window_slots}) AS med_gap, coalesce(gs.max_gap, 0) AS max_gap,
       coalesce(v.med_pay, v.med_all, 1) AS med_value, coalesce(r.reuse, 0) AS reuse
FROM b LEFT JOIN gs USING (unit) LEFT JOIN v USING (unit) LEFT JOIN r USING (unit)
ORDER BY b.unit
"""


def features(con: duckdb.DuckDBPyConnection, window_slots: int, slot_seconds: float = 1.0) -> pd.DataFrame:
    """One row per unit: the raw (untransformed) clustering features.

    Columns: unit, n_tx, tx_per_day, med_gap_s, med_value, script_share,
    stake_share, in_per_tx, out_per_tx, reuse, active_days, max_dormancy_s.
    """
    df = con.execute(FEATURE_SQL.format(window_slots=int(window_slots), slot_seconds=float(slot_seconds))).fetchdf()
    days = window_slots * slot_seconds / 86400.0
    df["tx_per_day"] = df["n_tx"] / days
    df["med_gap_s"] = df["med_gap"].astype(float) * slot_seconds
    df["max_dormancy_s"] = df["max_gap"].astype(float) * slot_seconds
    df["med_value"] = df["med_value"].astype(float)
    cols = ["unit", "n_tx", "tx_per_day", "med_gap_s", "med_value", "script_share", "stake_share",
            "in_per_tx", "out_per_tx", "reuse", "active_days", "max_dormancy_s"]
    return df[cols]
