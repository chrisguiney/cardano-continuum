#!/usr/bin/env python3
"""Summarise the humans of a fake run for guide.ipynb, as one small JSON file.

    continuum run --fake --seed 7 --wallets 100 --profiles mainnet --scale 200 --epochs 12 --out RUN
    python humans_run.py RUN --out tests/fixtures/humans/run.json

RUN holds the run's events.log and store.db. The log has every human born
after genesis (HumanBorn) and every payday, bill and trade; the store's
identity rows have each wallet's owner as of the last checkpoint, which is
the only record of the humans that existed at genesis (genesis spawns are
configuration, not decisions, so the log leaves them out).

The summary holds, sampled every BIN_MS of simulated time:

    wallets_per_human   how many humans own 1, 2, 3 ... wallets
    tiers               humans per wealth tier
    fiat_months         per tier, the median human's fiat in months of its
                        own pay (so currencies of any size share an axis)
    bought              ADA bought from income and by the wallets' own
                        agendas, per bin
    price               ADA/USD at each bin
    unemployed          the share of paydays out of work, per bin

Only the standard library is used, so it runs outside the lab image. The
output is byte-identical for the same run: keys sorted, numbers rounded.
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import statistics
import struct

# The guide's bin: five simulated minutes, the default month.
BIN_MS = 300_000
LOVELACE_PER_ADA = 1_000_000
# A frame is u32 length (big-endian) of version byte and payload, u32 CRC,
# version byte, JSON payload (internal/eventlog/record.go).
FRAME_HEAD = 8
DIGITS = 6


def records(path: str, kinds: set[str] | None = None):
    """The records of an event log, in order, as dicts: every one, or those
    whose kind is in kinds (the others are skipped undecoded)."""
    marks = [f'"kind":"{k}"'.encode() for k in kinds or ()]
    with open(path, "rb") as f:
        while True:
            head = f.read(FRAME_HEAD)
            if len(head) < FRAME_HEAD:
                return
            (n,) = struct.unpack(">I", head[:4])
            body = f.read(n)
            if len(body) < n:
                return
            if marks and not any(m in body for m in marks):
                continue
            yield json.loads(body[1:])


def owners(store: str) -> dict[int, dict]:
    """Each human's owner record (store.Owner) by human id, from the wallets'
    identity rows, with every wallet it owns."""
    out: dict[int, dict] = {}
    con = sqlite3.connect(f"file:{store}?mode=ro", uri=True)
    try:
        for (identity,) in con.execute("SELECT identity FROM actor WHERE identity != ''"):
            owner = json.loads(identity).get("owner")
            if owner:
                out[owner["human"]] = owner
    finally:
        con.close()
    return out


class Fiat:
    """One human's fiat over the run, rebuilt from its start and its flows."""

    def __init__(self, start: int, income: int, tier: str):
        self.now, self.income, self.tier = start, max(income, 1), tier

    def months(self) -> float:
        return self.now / self.income


def summarise(run: str) -> dict:
    """The guide's view of the humans of the run in directory run."""
    log = os.path.join(run, "events.log")
    people = owners(os.path.join(run, "store.db"))
    # A human born during the run enters the series at its HumanBorn; the
    # others existed from genesis.
    born = {r["body"]["human"] for r in records(log, {"HumanBorn"})}
    fiat = {h: Fiat(o["money"]["fiat0"], o["money"]["income"], o["tier"]) for h, o in people.items() if o.get("money") and h not in born}
    dca: set[int] = set()
    bins: dict[int, dict] = {}
    price: dict[int, float] = {}

    def at_bin(at: int) -> dict:
        b = at // BIN_MS
        return bins.setdefault(b, {"dca": 0, "scored": 0, "paydays": 0, "out": 0, "fiat": {}})

    snapshots: dict[int, dict[str, list[float]]] = {}
    current_bin = 0
    for r in records(log, {"HumanBorn", "Income", "Expense", "Invested", "Traded", "Quote"}):
        kind, body, at = r.get("kind"), r.get("body") or {}, r.get("at", 0)
        b = at // BIN_MS
        while current_bin < b:
            snapshots[current_bin] = by_tier(fiat)
            current_bin += 1
        if kind == "HumanBorn" and body.get("money"):
            o = people.get(body["human"], {})
            fiat[body["human"]] = Fiat(body["money"]["fiat"], body["money"]["income"], o.get("tier", body["tier"]))
        elif kind == "Income":
            f = fiat.get(body["human"])
            if f:
                f.now += body["cents"]
            row = at_bin(at)
            row["paydays"] += 1
            row["out"] += 0 if body.get("employed") else 1
        elif kind == "Expense" and body["human"] in fiat:
            fiat[body["human"]].now -= body["paid"]
        elif kind == "Invested":
            dca.add(body["intent"])
        elif kind == "Traded" and body.get("human"):
            f = fiat.get(body["human"])
            if f:
                f.now += body["cents"] if body["side"] == "sell" else -body["cents"]
            if body["side"] == "buy":
                at_bin(at)["dca" if body["intent"] in dca else "scored"] += body["lovelace"] / LOVELACE_PER_ADA
        elif kind == "Quote":
            price.setdefault(b, body["ada"])
    snapshots[current_bin] = by_tier(fiat)

    tiers: dict[str, int] = {}
    counts: dict[str, int] = {}
    for o in people.values():
        tiers[o["tier"]] = tiers.get(o["tier"], 0) + 1
        k = str(len(o["wallets"]))
        counts[k] = counts.get(k, 0) + 1

    names = sorted({t for s in snapshots.values() for t in s})
    return rounded({
        "bin_ms": BIN_MS,
        "humans": len(people),
        "wallets_per_human": counts,
        "tiers": tiers,
        "fiat_months": {t: [[b * BIN_MS, statistics.median(s[t])] for b, s in sorted(snapshots.items()) if s.get(t)] for t in names},
        "bought": [[b * BIN_MS, row["dca"], row["scored"]] for b, row in sorted(bins.items())],
        "price": [[b * BIN_MS, p] for b, p in sorted(price.items())],
        "unemployed": [[b * BIN_MS, row["out"] / row["paydays"]] for b, row in sorted(bins.items()) if row["paydays"]],
    })


def by_tier(fiat: dict[int, Fiat]) -> dict[str, list[float]]:
    """Every human's fiat in months of its pay, by tier."""
    out: dict[str, list[float]] = {}
    for f in fiat.values():
        out.setdefault(f.tier, []).append(f.months())
    return out


def rounded(value):
    """value with every float at DIGITS significant digits, recursively."""
    if isinstance(value, float):
        return float(f"{value:.{DIGITS}g}")
    if isinstance(value, dict):
        return {k: rounded(v) for k, v in value.items()}
    if isinstance(value, list):
        return [rounded(v) for v in value]
    return value


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("run", help="a fake run's directory: events.log and store.db")
    ap.add_argument("--out", required=True)
    args = ap.parse_args(argv)
    raw = json.dumps(summarise(args.run), sort_keys=True, separators=(",", ":")) + "\n"
    with open(args.out, "w") as f:
        f.write(raw)
    print(f"wrote {args.out} ({len(raw)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
