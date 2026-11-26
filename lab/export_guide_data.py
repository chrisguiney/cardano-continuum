#!/usr/bin/env python3
"""Export a fit as one small JSON file for the guide's interactive pages.

    python fit.py CHAIN --out OUT --clusters --network N --units-out OUT/N.units.parquet
    python export_guide_data.py OUT/N.target.json OUT/N.units.parquet --out ../docs/guide-data/N.json

The file holds, for one network:

    header      window, seed, k, BIC curve, sampling, scale factors
    clusters    each cluster's share, agenda, tx-type mix and fitted
                parameters (activity, gaps, dormancy, amount, betas)
    units       a stratified sample of real units: log10 transactions per
                day, log10 median payment in ADA, and cluster, for a scatter
    actors      the non-wallet populations' fitted rates and distributions

guide_data is pure (dicts and a DataFrame in, a dict out); main reads and
writes the files. The output is byte-identical for the same inputs: keys
are sorted, numbers rounded, and the unit sample is drawn with a seed from
units sorted by key.
"""

from __future__ import annotations

import argparse
import json
import math

import numpy as np
import pandas as pd

from cardano import LOVELACE_PER_ADA
from population_fit import dormancy_onset

# About this many units go in the scatter: enough to show each cluster's
# shape, few enough to keep the file well under MAX_BYTES.
SAMPLE_UNITS = 1500
# Every cluster gets at least this many points (or all its units), so a
# cluster of 0.2% still shows up instead of as one dot.
MIN_PER_CLUSTER = 25
# The guide loads one file per network in the browser.
MAX_BYTES = 300_000
# Six significant digits, as in the profile files: far finer than any fit
# is accurate, and stable under refits that change only the last bits.
DIGITS = 6
# Scatter coordinates are plotted, not computed with: four decimals of a
# log10 are a 0.01% step.
COORD_DECIMALS = 4

FORMAT_VERSION = 1


def rounded(value):
    """value with every float at DIGITS significant digits, recursively."""
    if isinstance(value, float):
        return float(f"{value:.{DIGITS}g}") if math.isfinite(value) else None
    if isinstance(value, dict):
        return {key: rounded(v) for key, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [rounded(v) for v in value]
    return value


def allocate(sizes: dict[str, int], total: int, floor: int) -> dict[str, int]:
    """How many units to draw from each cluster: in proportion to its size,
    at least `floor` (or its whole size when smaller), never more than it
    has. The proportional part is rounded down, so the sum can fall a few
    short of total; that keeps the rule simple and the file bounded."""
    whole = sum(sizes.values())
    return {name: min(size, max(floor, int(total * size / whole))) for name, size in sizes.items()}


def sample_units(units: pd.DataFrame, total: int, floor: int, seed: int) -> list[list]:
    """[log10 tx/day, log10 median payment ADA, cluster] for a stratified,
    seeded sample of units. units has columns unit, tx_per_day,
    median_payment_lovelace and cluster."""
    ordered = units.sort_values("unit", kind="mergesort").reset_index(drop=True)
    counts = allocate(ordered["cluster"].value_counts().to_dict(), total, floor)
    rng = np.random.default_rng(seed)
    rows = []
    for name in sorted(counts):
        members = np.flatnonzero((ordered["cluster"] == name).to_numpy())
        chosen = np.sort(rng.choice(members, size=counts[name], replace=False))
        for i in chosen:
            unit = ordered.iloc[i]
            rows.append([
                round(math.log10(unit["tx_per_day"]), COORD_DECIMALS),
                round(math.log10(max(unit["median_payment_lovelace"], 1.0) / LOVELACE_PER_ADA), COORD_DECIMALS),
                name,
            ])
    return rows


def cluster_entry(name: str, cluster: dict, agenda: str) -> dict:
    """One cluster's share, agenda, mix and fitted distribution parameters."""
    fit = cluster["fit"]
    activity, dormancy, amount = fit["activity"], fit["dormancy"], fit["amount"]
    return {
        "name": name,
        "share": cluster["share"],
        "agenda": agenda,
        "tx_type_mix": cluster["shares"]["tx_type_mix"],
        "weights": fit["weights"],
        "unreachable_share": fit["unreachable_share"],
        "activity": {
            "wakeups_per_sec": activity["base_per_sec"], "tail_index": activity["tail"],
            "active_fraction": activity["active_fraction"], "tx_probability": activity["tx_probability"],
        },
        "patience": {"dist": "pareto", "xm_seconds": fit["gap"]["xm"], "alpha": fit["gap"]["alpha"]},
        "dormancy": {
            "dist": "lognormal", "mu": dormancy["mu"], "sigma": dormancy["sigma"],
            "onset_per_sec": dormancy_onset(activity["active_fraction"], dormancy["mu"], dormancy["sigma"]),
        },
        "amount": {
            "body": {"dist": "lognormal", "mu": amount["mu"], "sigma": amount["sigma"]},
            "tail": {"dist": "pareto", "xm": amount["xm"], "alpha": amount["alpha"]},
            "tail_share": amount["tail_share"],
        },
        "competence": {"dist": "beta", "a": fit["competence"][0], "b": fit["competence"][1]},
        "care": {"dist": "beta", "a": fit["care"][0], "b": fit["care"][1]},
    }


HEADER_FIELDS = ("network", "window", "window_days", "window_first_slot", "window_last_slot", "seed", "k",
                 "k_gmm", "k_bic_min", "k_interior", "unit", "weighting", "units", "sampled_units", "sampling",
                 "pools", "dreps", "contracts", "mithril2pq_version", "mithril2pq_last_chunk")


def guide_data(target: dict, units: pd.DataFrame, total: int = SAMPLE_UNITS, floor: int = MIN_PER_CLUSTER) -> dict:
    """The guide's view of one fit: its target and its units."""
    header = target["header"]
    agendas = dict(item.split("=") for item in header.get("agendas", "").split(", ") if "=" in item)
    clusters = [cluster_entry(name, target["clusters"][name], agendas.get(name, "hodl"))
                for name in sorted(target["clusters"])]
    data = {
        "version": FORMAT_VERSION,
        "header": {key: header[key] for key in HEADER_FIELDS if key in header},
        "bic": {k: header["bic"][k] for k in sorted(header["bic"], key=int)},
        "clusters": clusters,
        "units": {"columns": ["log10_tx_per_day", "log10_median_payment_ada", "cluster"],
                  "rows": sample_units(units, total, floor, int(header["seed"]))},
        "actors": target.get("actors", {}),
    }
    return rounded(data)


def encode(data: dict) -> bytes:
    """The file's bytes: compact, keys sorted, so equal data is equal bytes."""
    return (json.dumps(data, sort_keys=True, separators=(",", ":")) + "\n").encode()


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("target", help="NETWORK.target.json written by fit.py --clusters")
    ap.add_argument("units", help="the units table written by fit.py --units-out")
    ap.add_argument("--out", required=True)
    ap.add_argument("--sample", type=int, default=SAMPLE_UNITS)
    args = ap.parse_args(argv)
    with open(args.target) as f:
        target = json.load(f)
    raw = encode(guide_data(target, pd.read_parquet(args.units), args.sample))
    if len(raw) > MAX_BYTES:
        raise SystemExit(f"{args.out}: {len(raw)} bytes is over {MAX_BYTES}; lower --sample")
    with open(args.out, "wb") as f:
        f.write(raw)
    print(f"wrote {args.out} ({len(raw)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
